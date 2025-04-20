// 分享相关
package share

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"sync"

	"github.com/jsyzchen/pan/conf"
	"github.com/jsyzchen/pan/utils/httpclient"
)

const SetUri = "/apaas/1.0/share/set?product=netdisk"
const VerifyUri = "/apaas/1.0/share/verify?product=netdisk"
const ListUri = "/apaas/1.0/share/list?product=netdisk"
const InfoUri = "/apaas/1.0/share/info?product=netdisk"
const TransferUri = "/apaas/1.0/share/transfer?product=netdisk"

type safeMap struct {
	sync.RWMutex
	m map[string]string
}

func (m *safeMap) get(key string) string {
	m.RLock()
	defer m.RUnlock()
	if v, ok := m.m[key]; ok {
		return v
	}
	return ""
}

func (m *safeMap) set(key, value string) {
	m.Lock()
	defer m.Unlock()
	m.m[key] = value
}

var spwdCache = &safeMap{
	m: make(map[string]string),
}

type ShareClient struct {
	AppId       string
	AccessToken string
}

func NewShareClient(appId, accessToken string) *ShareClient {
	return &ShareClient{
		AppId:       appId,
		AccessToken: accessToken,
	}
}

type BaseShareResponse struct {
	ErrorNo   int    `json:"errno"`
	RequestId string `json:"request_id"`
	Msg       string `json:"show_msg"`
}

type ShareLinkCreationData struct {
	ShortUrl string `json:"short_url"`
	Link     string `json:"link"`
	ShareId  uint64 `json:"share_id"`
	Period   int    `json:"period"`
	Pwd      string `json:"pwd"`
	Remark   string `json:"remark"`
}

type ShareLinkCreationResponse struct {
	BaseShareResponse
	Data ShareLinkCreationData `json:"data"`
}

type SharePwdVerificationData struct {
	Spwd string `json:"spwd"`
}

type SharePwdVerificationResponse struct {
	BaseShareResponse
	Data SharePwdVerificationData `json:"data"`
}

type ShareFileInfo struct {
	FsId       string `json:"fsid"`
	Category   int    `json:"category"`
	IsDir      int    `json:"isdir"`
	Name       string `json:"server_filename"`
	Path       string `json:"path"`
	Size       uint64 `json:"size"`
	CreateTime int64  `json:"server_ctime"`
	ModifyTime int64  `json:"server_mtime"`
	Md5        string `json:"md5"`
}

type ShareFilesData struct {
	Count int             `json:"count"`
	List  []ShareFileInfo `json:"list"`
}

type ShareFilesResponse struct {
	BaseShareResponse
	Data ShareFilesData `json:"data"`
}

type ShareLinkInfo struct {
	ShortUrl   string `json:"short_url"`
	Id         uint64 `json:"share_id"`
	Uk         uint64 `json:"share_uk"`
	Remark     string `json:"remark"`
	VdtLimit   string `json:"vdt_limit"`
	Period     int    `json:"period"`
	Password   string `json:"pwd"`
	Link       string `json:"link"`
	CreateTime int64  `json:"ctime"`
	ModifyTime int64  `json:"mtime"`
	AppId      int    `json:"appid"`
	Status     int    `json:"status"`
}

type ShareUserInfo struct {
	UserName string `json:"user_name"`
	Avatar   string `json:"user_avatar"`
}

type ShareInfoData struct {
	LinkInfo ShareLinkInfo `json:"link_info"`
	UserInfo ShareUserInfo `json:"share_user_info"`
}

type ShareInfoResponse struct {
	BaseShareResponse
	Data ShareInfoData `json:"data"`
}

// 创建分享链接
func (client *ShareClient) CreateShareLink(fsidList []uint64, period int, pwd, remark string) (ShareLinkCreationResponse, error) {
	ret := ShareLinkCreationResponse{}

	v := url.Values{}
	v.Add("appid", client.AppId)
	v.Add("access_token", client.AccessToken)
	query := v.Encode()

	v = url.Values{}
	fsidStrList := make([]string, len(fsidList))
	for i, id := range fsidList {
		fsidStrList[i] = strconv.FormatUint(id, 10)
	}
	jsonFsidList, err := json.Marshal(fsidStrList)
	if err != nil {
		log.Println("ShareClient.CreateShareLink json.Marshal failed, err = ", err)
		return ret, err
	}
	v.Add("fsid_list", string(jsonFsidList))
	v.Add("period", strconv.Itoa(period))
	v.Add("pwd", pwd)
	v.Add("remark", remark)
	body := v.Encode()

	requestUrl := conf.OpenApiDomain + SetUri + "&" + query
	resp, err := httpclient.Post(nil, requestUrl, map[string]string{}, body)
	if err != nil {
		log.Println("ShareClient.CreateShareLink httpclient.Post failed, err = ", err)
		return ret, err
	}
	if resp.StatusCode != 200 {
		return ret, errors.New(fmt.Sprintf("ShareClient.CreateShareLink HttpStatusCode is not equal to 200, httpStatusCode[%d], respBody[%s]", resp.StatusCode, string(resp.Body)))
	}
	if err := json.Unmarshal(resp.Body, &ret); err != nil {
		return ret, err
	}
	if ret.ErrorNo != 0 {
		return ret, errors.New(fmt.Sprintf("ShareClient.CreateShareLink errorNo = %d msg = %s", ret.ErrorNo, ret.Msg))
	}

	return ret, nil
}

// 获取加密提取码
func (client *ShareClient) GetSpwd(shortUrl, pwd string) (string, error) {
	if pwd == "" {
		return "", nil
	}

	spwd := spwdCache.get(shortUrl + pwd)
	if spwd != "" {
		return spwd, nil
	}

	v := url.Values{}
	v.Add("appid", client.AppId)
	v.Add("access_token", client.AccessToken)
	v.Add("short_url", shortUrl)
	query := v.Encode()
	v = url.Values{}
	v.Add("pwd", pwd)
	body := v.Encode()

	requestUrl := conf.OpenApiDomain + VerifyUri + "&" + query
	resp, err := httpclient.Post(nil, requestUrl, map[string]string{}, body)
	if err != nil {
		log.Println("ShareClient.GetSpwd httpclient.Post failed, err = ", err)
		return "", err
	}
	if resp.StatusCode != 200 {
		return "", errors.New(fmt.Sprintf("ShareClient.GetSpwd HttpStatusCode is not equal to 200, httpStatusCode[%d], respBody[%s]", resp.StatusCode, string(resp.Body)))
	}

	vfresp := SharePwdVerificationResponse{}
	if err := json.Unmarshal(resp.Body, &vfresp); err != nil {
		return "", err
	}
	if vfresp.ErrorNo != 0 {
		return "", errors.New(fmt.Sprintf("ShareClient.GetSpwd errorNo = %d msg = %s", vfresp.ErrorNo, vfresp.Msg))
	}

	spwdCache.set(shortUrl+pwd, vfresp.Data.Spwd)
	return vfresp.Data.Spwd, nil
}

// 获取文件列表
func (client *ShareClient) ListFiles(shortUrl, pwd, dir string, page, pageSize int) (ShareFilesResponse, error) {
	ret := ShareFilesResponse{}

	spwd, err := client.GetSpwd(shortUrl, pwd)
	if err != nil {
		return ret, err
	}

	v := url.Values{}
	v.Add("appid", client.AppId)
	v.Add("access_token", client.AccessToken)
	v.Add("short_url", shortUrl)
	query := v.Encode()

	v = url.Values{}
	if spwd != "" {
		v.Add("spwd", spwd)
	}
	if dir != "" {
		v.Add("dir", dir)
		v.Add("page", strconv.Itoa(page))
		v.Add("page_size", strconv.Itoa(pageSize))
	}
	body := v.Encode()

	requestUrl := conf.OpenApiDomain + ListUri + "&" + query
	resp, err := httpclient.Post(nil, requestUrl, map[string]string{}, body)
	if err != nil {
		log.Println("ShareClient.ListFiles httpclient.Post failed, err = ", err)
		return ret, err
	}
	if resp.StatusCode != 200 {
		return ret, errors.New(fmt.Sprintf("ShareClient.ListFiles HttpStatusCode is not equal to 200, httpStatusCode[%d], respBody[%s]", resp.StatusCode, string(resp.Body)))
	}
	if err := json.Unmarshal(resp.Body, &ret); err != nil {
		return ret, err
	}
	if ret.ErrorNo != 0 {
		return ret, errors.New(fmt.Sprintf("ShareClient.ListFiles errorNo = %d msg = %s", ret.ErrorNo, ret.Msg))
	}

	return ret, nil
}

// 分享信息
func (client *ShareClient) GetShareInfo(shortUrl, pwd string) (ShareInfoResponse, error) {
	ret := ShareInfoResponse{}

	spwd, err := client.GetSpwd(shortUrl, pwd)
	if err != nil {
		return ret, err
	}

	v := url.Values{}
	v.Add("appid", client.AppId)
	v.Add("access_token", client.AccessToken)
	v.Add("short_url", shortUrl)
	query := v.Encode()

	v = url.Values{}
	if spwd != "" {
		v.Add("spwd", spwd)
	}
	body := v.Encode()

	requestUrl := conf.OpenApiDomain + InfoUri + "&" + query
	resp, err := httpclient.Post(nil, requestUrl, map[string]string{}, body)
	if err != nil {
		log.Println("ShareClient.GetShareInfo httpclient.Post failed, err = ", err)
		return ret, err
	}
	if resp.StatusCode != 200 {
		return ret, errors.New(fmt.Sprintf("ShareClient.GetShareInfo HttpStatusCode is not equal to 200, httpStatusCode[%d], respBody[%s]", resp.StatusCode, string(resp.Body)))
	}
	if err := json.Unmarshal(resp.Body, &ret); err != nil {
		return ret, err
	}
	if ret.ErrorNo != 0 {
		return ret, errors.New(fmt.Sprintf("ShareClient.GetShareInfo errorNo = %d msg = %s", ret.ErrorNo, ret.Msg))
	}

	return ret, nil
}

// 文件转存
func (client *ShareClient) TransferFiles(shortUrl, pwd, path string, fsidList []uint64) (BaseShareResponse, error) {
	ret := BaseShareResponse{}

	spwd, err := client.GetSpwd(shortUrl, pwd)
	if err != nil {
		return ret, err
	}

	v := url.Values{}
	v.Add("appid", client.AppId)
	v.Add("access_token", client.AccessToken)
	v.Add("short_url", shortUrl)
	query := v.Encode()

	v = url.Values{}
	fsidStrList := make([]string, len(fsidList))
	for i, id := range fsidList {
		fsidStrList[i] = strconv.FormatUint(id, 10)
	}
	jsonFsidList, err := json.Marshal(fsidStrList)
	if err != nil {
		log.Println("ShareClient.TransferFiles json.Marshal failed, err = ", err)
		return ret, err
	}
	v.Add("fsid_list", string(jsonFsidList))
	v.Add("spwd", spwd)
	v.Add("to_path", path)
	v.Add("async", "2")
	v.Add("ondup", "fail")
	body := v.Encode()

	requestUrl := conf.OpenApiDomain + TransferUri + "&" + query
	resp, err := httpclient.Post(nil, requestUrl, map[string]string{}, body)
	if err != nil {
		log.Println("ShareClient.TransferFiles httpclient.Post failed, err = ", err)
		return ret, err
	}
	if resp.StatusCode != 200 {
		return ret, errors.New(fmt.Sprintf("ShareClient.TransferFiles HttpStatusCode is not equal to 200, httpStatusCode[%d], respBody[%s]", resp.StatusCode, string(resp.Body)))
	}
	if err := json.Unmarshal(resp.Body, &ret); err != nil {
		return ret, err
	}
	if ret.ErrorNo != 0 {
		return ret, errors.New(fmt.Sprintf("ShareClient.TransferFiles errorNo = %d msg = %s", ret.ErrorNo, ret.Msg))
	}

	return ret, nil
}
