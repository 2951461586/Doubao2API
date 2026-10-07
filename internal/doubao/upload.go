package doubao

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 上传相关常量，对齐桌面端 uploader 的取值：
//   - UPLOADER_TENANT_ID / UPLOADER_SCENE_ID.BOT_CHAT
//   - resource_center_model.ResourceType
//   - imageX top API 的固定参数与签名域
const (
	uploadTenantID     = "5"
	uploadSceneBotChat = "5"

	// ResourceTypeFile / ResourceTypeImage 对应 resource_center_model.ResourceType。
	ResourceTypeFile  = 1
	ResourceTypeImage = 2

	imageXService = "imagex"
	imageXRegion  = "cn-north-1"
	imageXVersion = "2018-08-01"

	// imageXTopPath 是豆包网关代理 imageX top API 的路径（桌面端 getHost() 的取值）。
	imageXTopPath = "/top/v1"
	// imageXTopURL 是 ApplyImageUpload / CommitImageUpload 的入口。
	imageXTopURL = UpstreamBase + imageXTopPath
	// imageXUploadPath 是对象上传路径模板 "{tosDomain}/upload/v1/{oid}" 的前缀。
	imageXUploadPath = "/upload/v1/"
)

// uploadAuthToken 是 prepare_upload 返回的 STS 凭证。
type uploadAuthToken struct {
	AccessKey    string `json:"access_key"`
	SecretKey    string `json:"secret_key"`
	SessionToken string `json:"session_token"`
	CurrentTime  string `json:"current_time"`
	ExpiredTime  string `json:"expired_time"`
}

// prepareUploadResp 是 /alice/resource/prepare_upload 的响应。
type prepareUploadResp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		ServiceID        string          `json:"service_id"`
		UploadHost       string          `json:"upload_host"`
		UploadPathPrefix string          `json:"upload_path_prefix"`
		UploadAuthToken  uploadAuthToken `json:"upload_auth_token"`
	} `json:"data"`
}

// imageXError 是 imageX top API 的错误结构。
type imageXError struct {
	Code    string
	Message string
	CodeN   int
}

// applyUploadResp 是 ApplyImageUpload 的响应。
type applyUploadResp struct {
	ResponseMetadata struct {
		Error *imageXError `json:"Error"`
	} `json:"ResponseMetadata"`
	Result struct {
		UploadAddress struct {
			StoreInfos []struct {
				StoreURI string `json:"StoreUri"`
				Auth     string `json:"Auth"`
				UploadID string `json:"UploadID"`
			} `json:"StoreInfos"`
			SessionKey   string            `json:"SessionKey"`
			UploadHosts  []string          `json:"UploadHosts"`
			UploadHeader map[string]string `json:"UploadHeader"`
		} `json:"UploadAddress"`
	} `json:"Result"`
}

// commitUploadResp 是 CommitImageUpload 的响应。
type commitUploadResp struct {
	ResponseMetadata struct {
		Error *imageXError `json:"Error"`
	} `json:"ResponseMetadata"`
	Result struct {
		Results []struct {
			URI       string `json:"Uri"`
			URIStatus int    `json:"UriStatus"`
		} `json:"Results"`
	} `json:"Result"`
}

// prepareUpload 申请一次上传凭证（STS + service_id + upload_host）。
func (c *Client) prepareUpload(ctx context.Context, a *Account, resourceType int) (*prepareUploadResp, error) {
	payload := map[string]any{
		"tenant_id":     uploadTenantID,
		"scene_id":      uploadSceneBotChat,
		"resource_type": resourceType,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	u := UpstreamBase + "/alice/resource/prepare_upload?" + SecurityParams(a).Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	setUpstreamHeaders(req, a)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("申请上传凭证失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var out prepareUploadResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("申请上传凭证响应解析失败 (HTTP %d): %s", resp.StatusCode, truncate(string(raw), 200))
	}
	if out.Code != 0 {
		return nil, fmt.Errorf("申请上传凭证被拒绝 (code=%d): %s", out.Code, truncate(out.Msg, 200))
	}
	if out.Data.UploadAuthToken.AccessKey == "" || out.Data.ServiceID == "" {
		return nil, fmt.Errorf("申请上传凭证返回不完整")
	}
	return &out, nil
}

// UploadResource 把一段字节上传到豆包资源中心，返回可直接写入
// content_block 的资源 uri（图片为 block_type=10052 的 image.uri）。
func (c *Client) UploadResource(ctx context.Context, a *Account, resourceType int, fileName, mime string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("上传内容为空")
	}
	prep, err := c.prepareUpload(ctx, a, resourceType)
	if err != nil {
		return "", err
	}

	store, sessionKey, err := c.applyImageUpload(ctx, prep.Data.ServiceID, prep.Data.UploadAuthToken, fileName, data)
	if err != nil {
		return "", err
	}
	if err := c.putObject(ctx, store.UploadHost, store.StoreURI, store.Auth, store.UploadHeader, mime, data); err != nil {
		return "", err
	}
	return c.commitImageUpload(ctx, prep.Data.ServiceID, sessionKey, prep.Data.UploadAuthToken)
}

// applyImageUpload 调用 imageX top API 申请上传地址。
func (c *Client) applyImageUpload(ctx context.Context, serviceID string, tok uploadAuthToken, fileName string, data []byte) (storeInfo, string, error) {
	params := map[string]string{
		"Action":       "ApplyImageUpload",
		"Version":      imageXVersion,
		"ServiceId":    serviceID,
		"NeedFallback": "true",
		"s":            RandomHex(8),
		"FileSize":     strconv.Itoa(len(data)),
	}
	// FileExtension 带前导点（对齐桌面端 getFileSuffix 的返回值）
	if ext := fileSuffix(fileName); ext != "" {
		params["FileExtension"] = "." + ext
	}

	raw, err := c.callTop(ctx, http.MethodGet, params, nil, tok)
	if err != nil {
		return storeInfo{}, "", fmt.Errorf("ApplyImageUpload 失败: %w", err)
	}
	var out applyUploadResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return storeInfo{}, "", fmt.Errorf("ApplyImageUpload 响应解析失败: %s", truncate(string(raw), 200))
	}
	if e := out.ResponseMetadata.Error; e != nil {
		return storeInfo{}, "", fmt.Errorf("ApplyImageUpload 被拒绝: %s", e.Message)
	}
	addr := out.Result.UploadAddress
	if len(addr.StoreInfos) == 0 || len(addr.UploadHosts) == 0 {
		return storeInfo{}, "", fmt.Errorf("ApplyImageUpload 未返回上传地址")
	}
	si := addr.StoreInfos[0]
	return storeInfo{
		StoreURI:     si.StoreURI,
		Auth:         si.Auth,
		UploadID:     si.UploadID,
		UploadHost:   addr.UploadHosts[0],
		UploadHeader: addr.UploadHeader,
	}, addr.SessionKey, nil
}

// storeInfo 是 ApplyImageUpload 返回的一次上传地址。
type storeInfo struct {
	StoreURI     string
	Auth         string
	UploadID     string
	UploadHost   string
	UploadHeader map[string]string
}

// putObject 把字节直传到对象存储（非 SigV4，使用 ApplyImageUpload 返回的 Auth）。
func (c *Client) putObject(ctx context.Context, host, storeURI, auth string, extra map[string]string, mime string, data []byte) error {
	u := "https://" + host + imageXUploadPath + storeURI
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	// TOS 的 Content-CRC32 是 8 位小写十六进制（对齐 SDK 的 dec2hex）
	req.Header.Set("Content-CRC32", fmt.Sprintf("%08x", crc32.ChecksumIEEE(data)))
	if mime != "" {
		req.Header.Set("Content-Type", mime)
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("上传对象失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("上传对象失败 (HTTP %d): %s", resp.StatusCode, truncate(string(raw), 200))
	}
	// 对象存储以 JSON 返回，成功 code 为 2000
	var r struct {
		Code int    `json:"code"`
		Msg  string `json:"message"`
	}
	if json.Unmarshal(raw, &r) == nil && r.Code != 0 && r.Code != 2000 {
		return fmt.Errorf("上传对象被拒绝 (code=%d): %s", r.Code, truncate(r.Msg, 200))
	}
	return nil
}

// commitImageUpload 提交上传，返回最终资源 uri。
func (c *Client) commitImageUpload(ctx context.Context, serviceID, sessionKey string, tok uploadAuthToken) (string, error) {
	params := map[string]string{
		"Action":    "CommitImageUpload",
		"Version":   imageXVersion,
		"ServiceId": serviceID,
	}
	body, err := json.Marshal(map[string]any{"SessionKey": sessionKey})
	if err != nil {
		return "", err
	}
	raw, err := c.callTop(ctx, http.MethodPost, params, body, tok)
	if err != nil {
		return "", fmt.Errorf("CommitImageUpload 失败: %w", err)
	}
	var out commitUploadResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("CommitImageUpload 响应解析失败: %s", truncate(string(raw), 200))
	}
	if e := out.ResponseMetadata.Error; e != nil {
		return "", fmt.Errorf("CommitImageUpload 被拒绝: %s", e.Message)
	}
	if len(out.Result.Results) == 0 || out.Result.Results[0].URI == "" {
		return "", fmt.Errorf("CommitImageUpload 未返回 uri")
	}
	return out.Result.Results[0].URI, nil
}

// callTop 调用 imageX top API（SigV4 签名）。
func (c *Client) callTop(ctx context.Context, method string, params map[string]string, body []byte, tok uploadAuthToken) ([]byte, error) {
	if tok.AccessKey == "" {
		return nil, fmt.Errorf("缺少上传凭证")
	}
	authz, extra := signV4(method, imageXTopPath, params, nil, body,
		tok.AccessKey, tok.SecretKey, tok.SessionToken, imageXRegion, imageXService, time.Now())

	u := imageXTopURL + "?" + encodeQuery(params)
	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", authz)
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", UserAgent)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	return raw, nil
}

// UploadImage 上传一张图片，返回可用于 content_block 的 uri。
func (c *Client) UploadImage(ctx context.Context, a *Account, img ImageAttachment) (string, error) {
	name := img.Name
	if name == "" {
		name = "image.png"
	}
	mime := img.Format
	if !strings.Contains(mime, "/") {
		mime = mimeByFormat(img.Format)
	}
	return c.UploadResource(ctx, a, ResourceTypeImage, name, mime, img.Data)
}

// --- 工具函数 ---

func bodyReader(b []byte) io.Reader {
	if b == nil {
		return nil
	}
	return bytes.NewReader(b)
}

// setUpstreamHeaders 写入调用豆包业务接口所需的公共请求头。
func setUpstreamHeaders(req *http.Request, a *Account) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Origin", UpstreamBase)
	req.Header.Set("Referer", UpstreamBase+"/chat/")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	if t := a.CSRFToken(); t != "" {
		req.Header.Set("x-tt-passport-csrf-token", t)
	}
	req.Header.Set("Cookie", CookieHeader(a.Cookies))
}

// encodeQuery 按 SigV4 规范拼接规范化查询串（键排序 + RFC3986 转义）。
func encodeQuery(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, rfc3986(k)+"="+rfc3986(params[k]))
	}
	return strings.Join(parts, "&")
}

// rfc3986 是 encodeURIComponent 的严格子集（未保留字符一律百分号转义）。
func rfc3986(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// isSignableHeader 判断某请求头是否参与 SigV4 签名。
//
// 对齐 @byted/uploader：x-amz-* 一律签名；content-type / content-length /
// user-agent 等不参与签名。
func isSignableHeader(lower string) bool {
	if strings.HasPrefix(lower, "x-amz-") {
		return true
	}
	switch lower {
	case "authorization", "content-type", "content-length", "user-agent",
		"presigned-expires", "expect", "x-amzn-trace-id":
		return false
	}
	return true
}

// signV4 按 AWS SigV4（ByteDance top API 变体）计算 Authorization 头。
//
// 返回签名值与需要随请求发送的附加头（X-Amz-Date / x-amz-security-token /
// X-Amz-Content-Sha256）。
func signV4(method, pathname string, params, headers map[string]string, body []byte,
	accessKey, secretKey, sessionToken, region, service string, now time.Time) (string, map[string]string) {

	amzDate := now.UTC().Format("20060102T150405Z")
	dateStamp := amzDate[:8]

	h := map[string]string{}
	for k, v := range headers {
		h[k] = v
	}
	h["X-Amz-Date"] = amzDate
	if sessionToken != "" {
		h["x-amz-security-token"] = sessionToken
	}
	emptyHash := sha256.Sum256(nil)
	bodyHash := hex.EncodeToString(emptyHash[:])
	if body != nil {
		sum := sha256.Sum256(body)
		bodyHash = hex.EncodeToString(sum[:])
		h["X-Amz-Content-Sha256"] = bodyHash
	}

	type kv struct{ k, v string }
	var items []kv
	for k, v := range h {
		lk := strings.ToLower(k)
		if !isSignableHeader(lk) {
			continue
		}
		items = append(items, kv{lk, strings.Join(strings.Fields(v), " ")})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].k < items[j].k })

	ch := make([]string, 0, len(items))
	sh := make([]string, 0, len(items))
	for _, it := range items {
		ch = append(ch, it.k+":"+it.v)
		sh = append(sh, it.k)
	}
	canonicalHeaders := strings.Join(ch, "\n")
	signedHeaders := strings.Join(sh, ";")

	canonicalRequest := strings.Join([]string{
		method, pathname, encodeQuery(params), canonicalHeaders + "\n", signedHeaders, bodyHash,
	}, "\n")

	crSum := sha256.Sum256([]byte(canonicalRequest))
	scope := dateStamp + "/" + region + "/" + service + "/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, scope, hex.EncodeToString(crSum[:]),
	}, "\n")

	kSigning := hmacSHA256(
		hmacSHA256(
			hmacSHA256(
				hmacSHA256([]byte("AWS4"+secretKey), dateStamp), region), service), "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))

	authz := "AWS4-HMAC-SHA256 Credential=" + accessKey + "/" + scope +
		", SignedHeaders=" + signedHeaders + ", Signature=" + signature
	return authz, h
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// fileSuffix 返回文件名的扩展名（不含点），无扩展名时返回空串。
func fileSuffix(name string) string {
	i := strings.LastIndex(name, ".")
	if i < 0 || i == len(name)-1 {
		return ""
	}
	ext := name[i+1:]
	if len(ext) > 8 {
		return ""
	}
	return ext
}

// mimeByFormat 由扩展名推断图片 MIME。
func mimeByFormat(format string) string {
	switch strings.ToLower(format) {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	case "bmp":
		return "image/bmp"
	case "heic":
		return "image/heic"
	case "", "png":
		return "image/png"
	default:
		return "image/" + strings.ToLower(format)
	}
}

// maxImageBytes 限制网关代下载图片的大小。
const maxImageBytes = 10 << 20

// FetchImage 下载外部图片，供网关代取后转存到上游。
//
// 返回原始字节与 MIME；大小超过 maxImageBytes 时拒绝。
func FetchImage(ctx context.Context, rawURL string) ([]byte, string, error) {
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return nil, "", fmt.Errorf("不支持的图片地址")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "image/*")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("下载图片失败 (HTTP %d)", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxImageBytes {
		return nil, "", fmt.Errorf("图片超过 %d MB 上限", maxImageBytes>>20)
	}
	if len(data) == 0 {
		return nil, "", fmt.Errorf("图片内容为空")
	}
	mime := resp.Header.Get("Content-Type")
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = mime[:i]
	}
	if !strings.HasPrefix(mime, "image/") {
		mime = http.DetectContentType(data)
	}
	return data, mime, nil
}

// ParseDataURL 解析 data:image/png;base64,xxxx 形式的内联图片。
func ParseDataURL(s string) (mime string, data []byte, ok bool) {
	if !strings.HasPrefix(s, "data:") {
		return "", nil, false
	}
	comma := strings.IndexByte(s, ',')
	if comma < 0 {
		return "", nil, false
	}
	head := s[len("data:"):comma]
	payload := s[comma+1:]
	if !strings.Contains(head, "base64") {
		if d, err := url.PathUnescape(payload); err == nil {
			return strings.TrimSuffix(head, ";base64"), []byte(d), true
		}
		return "", nil, false
	}
	mime = strings.TrimSuffix(head, ";base64")
	d, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", nil, false
	}
	return mime, d, true
}
