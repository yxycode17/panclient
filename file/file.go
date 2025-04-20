package file

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strconv"

	"github.com/jsyzchen/pan/conf"
	"github.com/jsyzchen/pan/utils/httpclient"
)

const (
	ListUri          = "/rest/2.0/xpan/file?method=list"
	ListRecursiveUri = "/rest/2.0/xpan/multimedia?method=listall"
	SearchUri        = "/rest/2.0/xpan/file?method=search"
	MetasUri         = "/rest/2.0/xpan/multimedia?method=filemetas"
	StreamingUri     = "/rest/2.0/xpan/file?method=streaming"
	ManagerUri       = "/rest/2.0/xpan/file?method=filemanager"
)

type FsItem struct {
	FsID           uint64            `json:"fs_id"`
	Path           string            `json:"path"`
	ServerFileName string            `json:"server_filename"`
	Size           uint64            `json:"size"`
	IsDir          int               `json:"isdir"`
	Category       int               `json:"category"`
	Md5            string            `json:"md5"`
	DirEmpty       int               `json:"dir_empty"`
	Thumbs         map[string]string `json:"thumbs"`
	LocalCtime     int64             `json:"local_ctime"`
	LocalMtime     int64             `json:"local_mtime"`
	ServerCtime    int64             `json:"server_ctime"`
	ServerMtime    int64             `json:"server_mtime"`
}

type ListResponse struct {
	conf.CloudDiskResponseBase
	List []FsItem
}

type ListRecursiveResponse struct {
	ErrorCode int    `json:"errno"`
	ErrorMsg  string `json:"errmsg"`
	Cursor    int    `json:"cursor"`
	HasMore   int    `json:"has_more"`
	List      []FsItem
}

type SearchResponse struct {
	conf.CloudDiskResponseBase
	HasMore int `json:"has_more"`
	List    []FsItem
}

type MetasResponse struct {
	ErrorCode    int    `json:"errno"`
	ErrorMsg     string `json:"errmsg"`
	RequestID    int
	RequestIDStr string `json:"request_id"`
	List         []struct {
		FsID        uint64            `json:"fs_id"`
		Path        string            `json:"path"`
		Category    int               `json:"category"`
		FileName    string            `json:"filename"`
		IsDir       int               `json:"isdir"`
		Size        int64             `json:"size"`
		Md5         string            `json:"md5"`
		DLink       string            `json:"dlink"`
		Thumbs      map[string]string `json:"thumbs"`
		ServerCtime int64             `json:"server_ctime"`
		ServerMtime int64             `json:"server_mtime"`
		DateTaken   int               `json:"date_taken"`
		Width       int               `json:"width"`
		Height      int               `json:"height"`
	}
}

type ManagerResponse struct {
	conf.CloudDiskResponseBase
	TaskId uint64 `json:"taskid"`
	Info   []struct {
		Path  string `json:"path"`
		Errno int    `json:"errno"`
	}
}

type CreateDirResponse struct {
	ErrorNo  int    `json:"errno"`
	FsId     uint64 `json:"fs_id"`
	Path     string `json:"path"`
	Category int    `json:"category"`
	IsDir    int    `json:"isdir"`
}

type File struct {
	AccessToken string
}

func NewFileClient(accessToken string) *File {
	return &File{
		AccessToken: accessToken,
	}
}

// 获取文件列表
func (f *File) List(dir string, start, limit int) (ListResponse, error) {
	ret := ListResponse{}

	v := url.Values{}
	v.Add("access_token", f.AccessToken)
	v.Add("dir", dir)
	v.Add("start", strconv.Itoa(start))
	v.Add("limit", strconv.Itoa(limit))
	query := v.Encode()

	requestUrl := conf.OpenApiDomain + ListUri + "&" + query
	resp, err := httpclient.Get(nil, requestUrl, map[string]string{})
	if err != nil {
		log.Println("httpclient.Get failed, err:", err)
		return ret, err
	}

	if resp.StatusCode != 200 {
		return ret, errors.New(fmt.Sprintf("HttpStatusCode is not equal to 200, httpStatusCode[%d], respBody[%s]", resp.StatusCode, string(resp.Body)))
	}

	if err := json.Unmarshal(resp.Body, &ret); err != nil {
		return ret, err
	}

	if ret.ErrorCode != 0 { //错误码不为0
		return ret, errors.New(fmt.Sprintf("error_code:%d, error_msg:%s", ret.ErrorCode, ret.ErrorMsg))
	}

	return ret, nil
}

// 递归获取文件列表
func (f *File) ListRecursive(dir string) ([]FsItem, error) {
	items := []FsItem{}
	listPageFunc := func(start int) (ListRecursiveResponse, error) {
		ret := ListRecursiveResponse{}
		v := url.Values{}
		v.Add("access_token", f.AccessToken)
		v.Add("path", dir)
		v.Add("order", "name")
		v.Add("start", strconv.Itoa(start))
		v.Add("recursion", "1")
		query := v.Encode()
		requestUrl := conf.OpenApiDomain + ListRecursiveUri + "&" + query
		resp, err := httpclient.Get(nil, requestUrl, map[string]string{})
		if err != nil {
			log.Printf("listPageFunc httpclient.Get failed start: %d err: %v", start, err)
			return ret, err
		}
		if resp.StatusCode != 200 {
			errStr := fmt.Sprintf("listPageFunc http code error start: %d code: %d", start, resp.StatusCode)
			log.Println(errStr)
			return ret, errors.New(errStr)
		}
		if err := json.Unmarshal(resp.Body, &ret); err != nil {
			return ret, err
		}
		if ret.ErrorCode != 0 { //错误码不为0
			return ret, errors.New(fmt.Sprintf("listPageFunc error_code: %d, error_msg: %s", ret.ErrorCode, ret.ErrorMsg))
		}
		return ret, nil
	}

	start := 0
	for {
		pageRet, err := listPageFunc(start)
		if err != nil {
			return items, err
		}
		log.Printf("listDirRecursive start: %d count: %d", start, len(pageRet.List))
		items = append(items, pageRet.List...)
		if pageRet.HasMore != 1 {
			break
		}
		start = pageRet.Cursor
	}

	return items, nil
}

// 搜索文件
func (f *File) Search(keyword, dir string, page int) (SearchResponse, error) {
	ret := SearchResponse{}

	v := url.Values{}
	v.Add("access_token", f.AccessToken)
	v.Add("key", keyword)
	v.Add("dir", dir)
	v.Add("recursion", "1")
	v.Add("page", strconv.Itoa(page))
	query := v.Encode()

	requestUrl := conf.OpenApiDomain + SearchUri + "&" + query
	resp, err := httpclient.Get(nil, requestUrl, map[string]string{})
	if err != nil {
		log.Println("httpclient.Get failed, err:", err)
		return ret, err
	}

	if resp.StatusCode != 200 {
		return ret, errors.New(fmt.Sprintf("HttpStatusCode is not equal to 200, httpStatusCode[%d], respBody[%s]", resp.StatusCode, string(resp.Body)))
	}

	if err := json.Unmarshal(resp.Body, &ret); err != nil {
		return ret, err
	}

	if ret.ErrorCode != 0 { //错误码不为0
		return ret, errors.New(fmt.Sprintf("error_code:%d, error_msg:%s", ret.ErrorCode, ret.ErrorMsg))
	}

	return ret, nil
}

// 通过FsID获取文件信息
func (f *File) Metas(fsIDs []uint64) (MetasResponse, error) {
	ret := MetasResponse{}

	fsIDsByte, err := json.Marshal(fsIDs)
	if err != nil {
		return ret, err
	}

	v := url.Values{}
	v.Add("access_token", f.AccessToken)
	v.Add("fsids", string(fsIDsByte))
	v.Add("dlink", "1")
	v.Add("thumb", "1")
	v.Add("extra", "1")
	query := v.Encode()

	requestUrl := conf.OpenApiDomain + MetasUri + "&" + query
	resp, err := httpclient.Get(nil, requestUrl, map[string]string{})
	if err != nil {
		log.Println("httpclient.Get failed, err:", err)
		return ret, err
	}

	if resp.StatusCode != 200 {
		return ret, errors.New(fmt.Sprintf("HttpStatusCode is not equal to 200, httpStatusCode[%d], respBody[%s]", resp.StatusCode, string(resp.Body)))
	}

	if err := json.Unmarshal(resp.Body, &ret); err != nil {
		return ret, err
	}

	if ret.ErrorCode != 0 { //错误码不为0
		return ret, errors.New(fmt.Sprintf("error_code:%d, error_msg:%s", ret.ErrorCode, ret.ErrorMsg))
	}

	ret.RequestID, _ = strconv.Atoi(ret.RequestIDStr)

	return ret, nil
}

// 获取音视频在线播放地址，转码类型有M3U8_AUTO_480=>视频ts、M3U8_FLV_264_480=>视频flv、M3U8_MP3_128=>音频mp3、M3U8_HLS_MP3_128=>音频ts
func (f *File) Streaming(path string, transcodingType string) (string, error) {
	ret := ""

	v := url.Values{}
	v.Add("access_token", f.AccessToken)
	v.Add("path", path)
	v.Add("type", transcodingType)
	query := v.Encode()

	requestUrl := conf.OpenApiDomain + StreamingUri + "&" + query
	resp, err := httpclient.Get(nil, requestUrl, map[string]string{})
	if err != nil {
		log.Println("httpclient.Get failed, err:", err)
		return ret, err
	}

	if resp.StatusCode != 200 {
		return ret, errors.New(fmt.Sprintf("HttpStatusCode is not equal to 200, httpStatusCode[%d], respBody[%s]", resp.StatusCode, string(resp.Body)))
	}

	return string(resp.Body), nil
}

// 文件管理
func (f *File) Manage(opera, tasks string) (ManagerResponse, error) {
	ret := ManagerResponse{}

	v := url.Values{}
	v.Add("access_token", f.AccessToken)
	v.Add("opera", opera)
	query := v.Encode()

	requestUrl := conf.OpenApiDomain + ManagerUri + "&" + query
	body := url.Values{}
	body.Add("async", "1")
	body.Add("filelist", tasks)
	body.Add("ondup", "newcopy")
	resp, err := httpclient.Post(nil, requestUrl, map[string]string{}, body.Encode())
	if err != nil {
		log.Println("httpclient.Get failed, err:", err)
		return ret, err
	}

	if resp.StatusCode != 200 {
		return ret, errors.New(fmt.Sprintf("HttpStatusCode is not equal to 200, httpStatusCode[%d], respBody[%s]", resp.StatusCode, string(resp.Body)))
	}

	if err := json.Unmarshal(resp.Body, &ret); err != nil {
		return ret, err
	}

	if ret.ErrorCode != 0 { //错误码不为0
		return ret, errors.New(fmt.Sprintf("error_code:%d, error_msg:%s", ret.ErrorCode, ret.ErrorMsg))
	}

	return ret, nil
}

// 新建文件夹
func (f *File) CreateDir(path string) (CreateDirResponse, error) {
	ret := CreateDirResponse{}

	v := url.Values{}
	v.Add("access_token", f.AccessToken)
	query := v.Encode()

	requestUrl := conf.OpenApiDomain + CreateUri + "&" + query
	body := url.Values{}
	body.Add("path", path)
	body.Add("isdir", "1")
	body.Add("mode", "1")
	resp, err := httpclient.Post(nil, requestUrl, map[string]string{}, body.Encode())
	if err != nil {
		log.Println("File.CreateDir httpclient.Get failed, err:", err)
		return ret, err
	}

	if resp.StatusCode != 200 {
		return ret, errors.New(fmt.Sprintf("File.CreateDir HttpStatusCode is not equal to 200, httpStatusCode[%d], respBody[%s]", resp.StatusCode, string(resp.Body)))
	}

	if err := json.Unmarshal(resp.Body, &ret); err != nil {
		return ret, err
	}

	if ret.ErrorNo != 0 {
		return ret, errors.New(fmt.Sprintf("File.CreateDir errorNo = %d", ret.ErrorNo))
	}

	return ret, nil
}
