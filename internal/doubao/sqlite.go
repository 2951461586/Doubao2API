package doubao

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// 极简 SQLite 只读解析器。
//
// 只做一件事：读取 Chromium Cookies 库里某张 rowid 表的全部行。
// 刻意不引入任何第三方依赖（AStudio2API 的零依赖约定），
// 因此仅实现解析所需的 b-tree 遍历、溢出页与记录解码。

type sqliteDB struct {
	data     []byte
	pageSize int
	usable   int
}

// sqlValue 是 SQLite 记录中的一个值。
type sqlValue struct {
	Type  int // 0=null 1=int 2=float 3=text 4=blob
	Int   int64
	Float float64
	Text  string
	Blob  []byte
}

// AsString 以字符串形式返回该值。
func (v sqlValue) AsString() string {
	switch v.Type {
	case 3:
		return v.Text
	case 4:
		return string(v.Blob)
	case 1:
		return fmt.Sprintf("%d", v.Int)
	case 2:
		return fmt.Sprintf("%v", v.Float)
	}
	return ""
}

// AsBytes 以字节形式返回该值。
func (v sqlValue) AsBytes() []byte {
	switch v.Type {
	case 4:
		return v.Blob
	case 3:
		return []byte(v.Text)
	}
	return nil
}

// IsNull 报告该值是否为 NULL。
func (v sqlValue) IsNull() bool { return v.Type == 0 }

const sqliteHeaderSize = 100

// openSQLite 解析 SQLite 文件头。
func openSQLite(path string) (*sqliteDB, error) {
	data, err := readFileAll(path)
	if err != nil {
		return nil, err
	}
	if len(data) < sqliteHeaderSize {
		return nil, errors.New("sqlite: file too small")
	}
	if string(data[:16]) != "SQLite format 3\x00" {
		return nil, errors.New("sqlite: bad magic header")
	}
	ps := int(binary.BigEndian.Uint16(data[16:18]))
	if ps == 1 {
		ps = 65536
	}
	if ps < 512 || ps > 65536 || ps&(ps-1) != 0 {
		return nil, fmt.Errorf("sqlite: invalid page size %d", ps)
	}
	reserved := int(data[20])
	return &sqliteDB{data: data, pageSize: ps, usable: ps - reserved}, nil
}

// page 返回 1-based 页号的页数据。
func (db *sqliteDB) page(n int) ([]byte, error) {
	off := (n - 1) * db.pageSize
	if n < 1 || off+db.pageSize > len(db.data) {
		return nil, fmt.Errorf("sqlite: page %d out of range", n)
	}
	return db.data[off : off+db.pageSize], nil
}

// varint 解析 SQLite 变长整数。
func varint(b []byte, off int) (uint64, int) {
	var v uint64
	for i := 0; i < 8; i++ {
		if off+i >= len(b) {
			return v, len(b)
		}
		c := b[off+i]
		v = (v << 7) | uint64(c&0x7f)
		if c < 0x80 {
			return v, off + i + 1
		}
	}
	if off+8 >= len(b) {
		return v, len(b)
	}
	v = (v << 8) | uint64(b[off+8])
	return v, off + 9
}

// tableRoot 在 sqlite_master 中查找指定表的根页号。
func (db *sqliteDB) tableRoot(name string) (int, error) {
	rows, err := db.scanTree(1)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		if len(r) < 4 {
			continue
		}
		// sqlite_master 列：type, name, tbl_name, rootpage, sql
		if r[0].AsString() == "table" && r[1].AsString() == name {
			return int(r[3].Int), nil
		}
	}
	return 0, fmt.Errorf("sqlite: table %q not found", name)
}

// scanTree 遍历以 root 为根的 b-tree，返回全部行（仅表 b-tree）。
func (db *sqliteDB) scanTree(root int) ([][]sqlValue, error) {
	var out [][]sqlValue
	seen := map[int]bool{}
	var walk func(n int) error
	walk = func(n int) error {
		if seen[n] {
			return nil
		}
		seen[n] = true
		p, err := db.page(n)
		if err != nil {
			return err
		}
		// 页 1 前 100 字节是文件头，b-tree 头紧随其后
		hdr := 0
		if n == 1 {
			hdr = sqliteHeaderSize
		}
		if hdr+8 > len(p) {
			return errors.New("sqlite: truncated page header")
		}
		typ := p[hdr]
		nCells := int(binary.BigEndian.Uint16(p[hdr+3 : hdr+5]))
		cellPtrOff := hdr + 8
		if typ == 0x05 {
			cellPtrOff = hdr + 12
		}
		if cellPtrOff+2*nCells > len(p) {
			return errors.New("sqlite: bad cell pointer array")
		}
		ptrs := make([]int, nCells)
		for i := 0; i < nCells; i++ {
			ptrs[i] = int(binary.BigEndian.Uint16(p[cellPtrOff+2*i : cellPtrOff+2*i+2]))
		}
		switch typ {
		case 0x05: // 内部表页
			for _, off := range ptrs {
				if off+4 > len(p) {
					return errors.New("sqlite: bad interior cell")
				}
				child := int(binary.BigEndian.Uint32(p[off : off+4]))
				if err := walk(child); err != nil {
					return err
				}
			}
			// 最右子指针
			if hdr+8 <= len(p) {
				right := int(binary.BigEndian.Uint32(p[hdr+8 : hdr+12]))
				if right > 0 {
					if err := walk(right); err != nil {
						return err
					}
				}
			}
		case 0x0D: // 叶子表页
			for _, off := range ptrs {
				rec, err := db.leafCell(p, off)
				if err != nil {
					continue // 单行损坏不放弃整表
				}
				out = append(out, rec)
			}
		default:
			return fmt.Errorf("sqlite: unsupported page type 0x%02x", typ)
		}
		return nil
	}
	if err := walk(root); err != nil {
		return nil, err
	}
	return out, nil
}

// leafCell 解析一个叶子表单元（含溢出页续链）。
func (db *sqliteDB) leafCell(p []byte, off int) ([]sqlValue, error) {
	payloadLen, o := varint(p, off)
	if o >= len(p) {
		return nil, errors.New("sqlite: bad payload len")
	}
	_, o = varint(p, o) // rowid，本用途不需要
	if o >= len(p) {
		return nil, errors.New("sqlite: bad rowid")
	}
	usable := db.usable
	maxLocal := usable - 35
	pl := int(payloadLen)
	avail := len(p) - o

	if pl <= avail && pl <= maxLocal {
		return decodeRecord(p[o : o+pl])
	}
	// 需要拼接溢出页
	local := maxLocal
	if pl > maxLocal && maxLocal > 0 {
		// 计算本地大小
		k := usable - 35
		// SQLite 公式：nLocal = k + (pl - k) % (usable - 4)，若超出则取 k
		local = k
		rest := pl - k
		if rest > 0 {
			mod := rest % (usable - 4)
			if k+mod <= usable-4 {
				local = k + mod
			}
		}
	}
	if local > avail {
		local = avail
	}
	buf := make([]byte, 0, pl)
	buf = append(buf, p[o:o+local]...)
	// 溢出页起始页号紧跟在本地数据之后
	nextOff := o + local
	if nextOff+4 > len(p) {
		return nil, errors.New("sqlite: missing overflow pointer")
	}
	next := int(binary.BigEndian.Uint32(p[nextOff : nextOff+4]))
	for next > 0 && len(buf) < pl {
		op, err := db.page(next)
		if err != nil {
			return nil, err
		}
		next = int(binary.BigEndian.Uint32(op[0:4]))
		take := usable - 4
		if remain := pl - len(buf); remain < take {
			take = remain
		}
		if 4+take > len(op) {
			take = len(op) - 4
		}
		buf = append(buf, op[4:4+take]...)
	}
	if len(buf) < pl {
		return nil, errors.New("sqlite: overflow truncated")
	}
	return decodeRecord(buf[:pl])
}

// decodeRecord 解码一条记录（serial type 序列 + 值区）。
func decodeRecord(rec []byte) ([]sqlValue, error) {
	if len(rec) == 0 {
		return nil, errors.New("sqlite: empty record")
	}
	hdrSize, off := varint(rec, 0)
	if int(hdrSize) > len(rec) {
		return nil, errors.New("sqlite: bad record header size")
	}
	var types []uint64
	for off < int(hdrSize) {
		t, n := varint(rec, off)
		if n <= off {
			break
		}
		types = append(types, t)
		off = n
	}
	vals := make([]sqlValue, 0, len(types))
	body := int(hdrSize)
	for _, t := range types {
		switch {
		case t == 0:
			vals = append(vals, sqlValue{Type: 0})
		case t >= 1 && t <= 6:
			n := []int{0, 1, 2, 3, 4, 6, 8}[t]
			if body+n > len(rec) {
				return nil, errors.New("sqlite: truncated int")
			}
			var v int64
			for i := 0; i < n; i++ {
				v = v<<8 | int64(rec[body+i])
			}
			// 符号扩展
			shift := uint(64 - 8*n)
			v = v << shift >> shift
			vals = append(vals, sqlValue{Type: 1, Int: v})
			body += n
		case t == 7:
			if body+8 > len(rec) {
				return nil, errors.New("sqlite: truncated float")
			}
			bits := binary.BigEndian.Uint64(rec[body : body+8])
			vals = append(vals, sqlValue{Type: 2, Float: float64FromBits(bits)})
			body += 8
		case t == 8:
			vals = append(vals, sqlValue{Type: 1, Int: 0})
		case t == 9:
			vals = append(vals, sqlValue{Type: 1, Int: 1})
		case t >= 12:
			var n int
			isBlob := t%2 == 0
			if isBlob {
				n = int((t - 12) / 2)
			} else {
				n = int((t - 13) / 2)
			}
			if body+n > len(rec) {
				return nil, errors.New("sqlite: truncated value")
			}
			raw := rec[body : body+n]
			if isBlob {
				vals = append(vals, sqlValue{Type: 4, Blob: raw})
			} else {
				vals = append(vals, sqlValue{Type: 3, Text: string(raw)})
			}
			body += n
		default:
			return nil, fmt.Errorf("sqlite: unsupported serial type %d", t)
		}
	}
	return vals, nil
}
