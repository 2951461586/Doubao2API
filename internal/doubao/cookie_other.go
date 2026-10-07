//go:build !windows

package doubao

import "fmt"

// 非 Windows 平台无法调用 DPAPI，因此不支持从桌面端自动导入。
// 仍可通过手工粘贴 Cookie 串添加账号。

// ImportResult 是一次桌面端导入的结果。
type ImportResult struct {
	Account *Account
	Profile string
	Dir     string
	Cookies int
}

// ImportFromDesktop 在非 Windows 平台不可用。
func ImportFromDesktop(string) (*ImportResult, error) {
	return nil, fmt.Errorf("桌面端自动导入仅支持 Windows（DPAPI 不可用）")
}

// ImportAllFromDesktop 在非 Windows 平台不可用。
func ImportAllFromDesktop(string) ([]*ImportResult, error) {
	return nil, fmt.Errorf("桌面端自动导入仅支持 Windows（DPAPI 不可用）")
}
