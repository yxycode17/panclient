package file

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bitly/go-simplejson"
	"github.com/jsyzchen/pan/account"
	"github.com/jsyzchen/pan/conf"
	fileUtil "github.com/jsyzchen/pan/utils/file"
	"github.com/jsyzchen/pan/utils/httpclient"
)

type UploadProgressHandler = func(int, int64, int64)

type UploadResponse struct {
	conf.CloudDiskResponseBase
	Path  string `json:"path"`
	Name  string `json:"server_filename"`
	Size  int64  `json:"size"`
	Md5   string `json:"md5"`
	FsID  uint64 `json:"fs_id"`
	IsDir int    `json:"isdir"`
}

type PreCreateResponse struct {
	conf.CloudDiskResponseBase
	UploadID   string         `json:"uploadid"`
	Path       string         `json:"path"`
	ReturnType int            `json:"return_type"`
	BlockList  []int          `json:"block_list"`
	Info       UploadResponse `json:"info"`
}

type SuperFile2UploadResponse struct {
	conf.PcsResponseBase
	Md5      string `json:"md5"`
	UploadID string `json:"uploadid"`
	PartSeq  string `json:"partseq"` //pcsapi PHP版本返回的是int类型，Go版本返回的是string类型
}

type UploadPartResponse struct {
	Response SuperFile2UploadResponse
	Size     int64
	Error    error
}

type LocalFileInfo struct {
	Md5     string
	Size    int64
	ModTime int64
}

type Uploader struct {
	AccessToken   string
	Path          string
	LocalFilePath string
	FileInfo      LocalFileInfo
	SliceSize     int64
}

const (
	PreCreateUri        = "/rest/2.0/xpan/file?method=precreate"
	CreateUri           = "/rest/2.0/xpan/file?method=create"
	Superfile2UploadUri = "/rest/2.0/pcs/superfile2?method=upload"
)

var UploadLock sync.Mutex

func NewUploader(accessToken, path, localFilePath string) *Uploader {
	return &Uploader{
		AccessToken:   accessToken,
		Path:          handleSpecialChar(path), // 处理特殊字符
		LocalFilePath: localFilePath,
	}
}

// 上传文件到网盘，包括预创建、分片上传、创建3个步骤
func (u *Uploader) Upload(ctx context.Context, progressHandler UploadProgressHandler) (UploadResponse, fileUtil.UploadSnapshot, error) {
	var ret UploadResponse
	retSnapshot := fileUtil.UploadSnapshot{}
	retSnapshot.Path = u.Path
	retSnapshot.LocalPath = u.LocalFilePath

	//1. file precreate
	preCreateRes, err := u.PreCreate(ctx, progressHandler)
	if err != nil {
		log.Println("PreCreate failed, err: ", err)
		ret.ErrorCode = preCreateRes.ErrorCode
		ret.ErrorMsg = preCreateRes.ErrorMsg
		ret.RequestID = preCreateRes.RequestID
		return ret, retSnapshot, err
	}
	retSnapshot.FileMd5 = u.FileInfo.Md5
	retSnapshot.FileModTime = u.FileInfo.ModTime
	retSnapshot.UploadId = preCreateRes.UploadID

	if preCreateRes.ReturnType == 2 { //云端已存在相同文件，直接上传成功，无需请求后面的分片上传和创建文件接口
		preCreateRes.Info.ErrorCode = preCreateRes.ErrorCode
		preCreateRes.Info.ErrorMsg = preCreateRes.ErrorMsg
		preCreateRes.Info.RequestID = preCreateRes.RequestID
		progressHandler(2, preCreateRes.Info.Size, preCreateRes.Info.Size)
		retSnapshot.DoneSize = preCreateRes.Info.Size
		retSnapshot.TotalSize = preCreateRes.Info.Size
		return preCreateRes.Info, retSnapshot, nil
	}
	uploadID := preCreateRes.UploadID

	UploadLock.Lock()
	defer UploadLock.Unlock()

	//2. superfile2 upload
	fileInfo, _ := u.GetFileInfo(false)
	retSnapshot.TotalSize = fileInfo.Size
	fileSize := fileInfo.Size
	sliceSize, err := u.GetSliceSize(fileSize)
	if err != nil {
		log.Println("GetSliceSize failed, err: ", err)
		return ret, retSnapshot, err
	}

	sliceNum := int(math.Ceil(float64(fileSize) / float64(sliceSize)))
	retSnapshot.SliceSize = sliceSize
	retSnapshot.SliceNum = sliceNum
	var doneSize int64 = 0
	var progressLock sync.Mutex
	progressTick := time.Now()
	internalProgressHandler := func(size int64) {
		progressLock.Lock()
		defer progressLock.Unlock()
		doneSize += size
		if doneSize > fileSize {
			doneSize = fileSize
		}
		oldTick := progressTick
		newTick := time.Now()
		if newTick.Sub(oldTick).Milliseconds() >= 500 || doneSize == fileSize {
			progressHandler(2, doneSize, fileSize)
			progressTick = newTick
		}
	}
	localFile, err := os.Open(u.LocalFilePath)
	if err != nil {
		log.Printf("upload os.Open failed localPath: %s err: %v", u.LocalFilePath, err)
		return ret, retSnapshot, err
	}
	defer localFile.Close()
	uploadRespChan := make(chan UploadPartResponse, sliceNum)
	sem := make(chan int, 2) //限制并发数，以防大文件上传导致占用服务器大量内存
	hasFailed := false
	uploadSliceNum := 0
	var uploadErr error
	for i := 0; i < sliceNum; i++ {
		if hasFailed {
			break
		}
		select {
		case <-ctx.Done():
			uploadErr = ctx.Err()
		default:
			break
		}
		if uploadErr != nil {
			break
		}
		buffer := make([]byte, sliceSize)
		n, err := localFile.Read(buffer[:])
		if err != nil && err != io.EOF {
			log.Printf("upload file.Read failed seq: %d localPath: %s err: %v", i, u.LocalFilePath, err)
			uploadErr = err
			break
		}
		if n == 0 { //文件已读取结束
			break
		}
		sem <- 1 //当通道已满的时候将被阻塞
		go func(partSeq int, partByte []byte) {
			uploadResp, err := u.TrySuperFile2Upload(ctx, uploadID, partSeq, partByte, internalProgressHandler)
			if err != nil {
				log.Printf("upload TrySuperFile2Upload failed seq: %d path: %s err: %v", partSeq, u.Path, err)
				hasFailed = true
			}
			uploadRespChan <- UploadPartResponse{uploadResp, int64(len(partByte)), err}
			<-sem
		}(i, buffer[0:n])
		uploadSliceNum++
	}

	blockList := make([]string, sliceNum)
	retSnapshot.Recoverable = true
	retSnapshot.DoneSlices = make([]string, sliceNum)
	for i := 0; i < uploadSliceNum; i++ {
		partResp := <-uploadRespChan
		if partResp.Error != nil {
			ret.ErrorCode = partResp.Response.ErrorCode
			ret.ErrorMsg = partResp.Response.ErrorMsg
			ret.RequestID = partResp.Response.RequestID
			if uploadErr == nil {
				uploadErr = partResp.Error
			}
			continue
		}
		partSeq, err := strconv.Atoi(partResp.Response.PartSeq)
		if err != nil {
			if uploadErr == nil {
				uploadErr = err
			}
			continue
		}
		blockList[partSeq] = partResp.Response.Md5
		retSnapshot.DoneSlices[partSeq] = partResp.Response.Md5
		retSnapshot.DoneSize += partResp.Size
		log.Printf("upload done seq: %d partSize: %d doneSize: %d totalSize: %d path: %s", partSeq, partResp.Size, retSnapshot.DoneSize, retSnapshot.TotalSize, u.Path)
	}
	if uploadErr != nil {
		return ret, retSnapshot, uploadErr
	}

	//3. file create
	superFile2CommitRes, err := u.Create(ctx, uploadID, blockList)
	if err != nil {
		log.Printf("upload SuperFile2Commit failed path: %s err: %v", u.Path, err)
		return superFile2CommitRes, retSnapshot, err
	}

	retSnapshot.Recoverable = false
	return superFile2CommitRes, retSnapshot, nil
}

// 从断点继续上传文件到网盘
func (u *Uploader) ResumeUpload(ctx context.Context, snapshot fileUtil.UploadSnapshot, progressHandler UploadProgressHandler) (UploadResponse, fileUtil.UploadSnapshot, error) {
	UploadLock.Lock()
	defer UploadLock.Unlock()

	var ret UploadResponse
	retSnapshot := snapshot
	retSnapshot.DoneSlices = make([]string, snapshot.SliceNum)
	copy(retSnapshot.DoneSlices, snapshot.DoneSlices)
	doneSize := snapshot.DoneSize
	var progressLock sync.Mutex
	progressTick := time.Now()
	internalProgressHandler := func(size int64) {
		progressLock.Lock()
		defer progressLock.Unlock()
		doneSize += size
		if doneSize > retSnapshot.TotalSize {
			doneSize = retSnapshot.TotalSize
		}
		oldTick := progressTick
		newTick := time.Now()
		if newTick.Sub(oldTick).Milliseconds() >= 500 || doneSize == retSnapshot.TotalSize {
			progressHandler(2, doneSize, retSnapshot.TotalSize)
			progressTick = newTick
		}
	}
	localFile, err := os.Open(u.LocalFilePath)
	if err != nil {
		log.Printf("resumeUpload os.Open failed localPath: %s err: %v", u.LocalFilePath, err)
		return ret, retSnapshot, err
	}
	defer localFile.Close()
	sliceNum := retSnapshot.SliceNum
	uploadRespChan := make(chan UploadPartResponse, sliceNum)
	sem := make(chan int, 2) //限制并发数，以防大文件上传导致占用服务器大量内存
	hasFailed := false
	uploadSliceNum := 0
	var offset int64 = 0
	var uploadErr error
	for i := 0; i < sliceNum; i++ {
		if hasFailed {
			break
		}
		select {
		case <-ctx.Done():
			uploadErr = ctx.Err()
		default:
			break
		}
		if uploadErr != nil {
			break
		}
		if retSnapshot.DoneSlices[i] != "" {
			offset += retSnapshot.SliceSize
			continue
		}
		localFile.Seek(offset, 0)
		buffer := make([]byte, snapshot.SliceSize)
		n, err := localFile.Read(buffer[:])
		offset += int64(n)
		if err != nil && err != io.EOF {
			log.Printf("resumeUpload file.Read failed seq: %d localPath: %s err: %v", i, u.LocalFilePath, err)
			uploadErr = err
			break
		}
		if n == 0 { //文件已读取结束
			break
		}
		sem <- 1 //当通道已满的时候将被阻塞
		go func(partSeq int, partByte []byte) {
			uploadResp, err := u.TrySuperFile2Upload(ctx, retSnapshot.UploadId, partSeq, partByte, internalProgressHandler)
			if err != nil {
				log.Printf("resumeUpload TrySuperFile2UploadFailed seq: %d path: %s err: %v", partSeq, u.Path, err)
				hasFailed = true
			}
			uploadRespChan <- UploadPartResponse{uploadResp, int64(len(partByte)), err}
			<-sem
		}(i, buffer[0:n])
		uploadSliceNum++
	}

	for i := 0; i < uploadSliceNum; i++ {
		partResp := <-uploadRespChan
		if partResp.Error != nil {
			ret.ErrorCode = partResp.Response.ErrorCode
			ret.ErrorMsg = partResp.Response.ErrorMsg
			ret.RequestID = partResp.Response.RequestID
			if uploadErr == nil {
				uploadErr = partResp.Error
			}
			continue
		}
		partSeq, err := strconv.Atoi(partResp.Response.PartSeq)
		if err != nil {
			if uploadErr == nil {
				uploadErr = err
			}
			continue
		}
		retSnapshot.DoneSlices[partSeq] = partResp.Response.Md5
		retSnapshot.DoneSize += partResp.Size
		log.Printf("resumeUpload done seq: %d partSize: %d doneSize: %d totalSize: %d path: %s", partSeq, partResp.Size, retSnapshot.DoneSize, retSnapshot.TotalSize, u.Path)
	}
	if uploadErr != nil {
		return ret, retSnapshot, uploadErr
	}

	blockList := make([]string, sliceNum)
	copy(blockList, retSnapshot.DoneSlices)
	superFile2CommitRes, err := u.Create(ctx, retSnapshot.UploadId, blockList)
	if err != nil {
		log.Printf("resumeUpload SuperFile2Commit failed path: %s err: %v", u.Path, err)
		return superFile2CommitRes, retSnapshot, err
	}

	retSnapshot.Recoverable = false
	return superFile2CommitRes, retSnapshot, nil
}

// preCreate
func (u *Uploader) PreCreate(ctx context.Context, progressHandler UploadProgressHandler) (PreCreateResponse, error) {
	ret := PreCreateResponse{}

	fileInfo, err := u.GetFileInfo(false)
	if err != nil {
		log.Println("GetFileInfo failed, err: ", err)
		return ret, err
	}
	fileSize := fileInfo.Size
	fileMd5 := fileInfo.Md5
	sliceMd5, err := u.getSliceMd5()
	if err != nil {
		log.Println("getSliceMd5 failed, err: ", err)
		return ret, err
	}

	progressTick := time.Now()
	var doneSize int64 = 0
	internalProgressHandler := func(size int64) {
		doneSize += size
		oldTick := progressTick
		newTick := time.Now()
		if newTick.Sub(oldTick).Milliseconds() >= 500 || doneSize == 0 || doneSize == fileSize {
			progressHandler(1, doneSize, fileSize)
			progressTick = newTick
		}
	}
	internalProgressHandler(0)

	blockList, err := u.getBlockList(ctx, internalProgressHandler)
	if err != nil {
		log.Println("getBlockList failed, err: ", err)
		return ret, err
	}
	blockListByte, err := json.Marshal(blockList)
	if err != nil {
		return ret, err
	}
	blockListStr := string(blockListByte)

	// path urlencode
	v := url.Values{}
	v.Add("path", u.Path)
	v.Add("size", strconv.FormatInt(fileSize, 10))
	v.Add("isdir", "0")
	v.Add("autoinit", "1") // 固定值1
	v.Add("rtype", "3")    // 3为覆盖
	v.Add("block_list", blockListStr)
	v.Add("content-md5", fileMd5)
	v.Add("slice-md5", sliceMd5)
	body := v.Encode()

	requestUrl := conf.OpenApiDomain + PreCreateUri + "&access_token=" + u.AccessToken
	headers := make(map[string]string)
	resp, err := httpclient.Post(ctx, requestUrl, headers, body)
	if err != nil {
		log.Println("httpclient.Post failed, err: ", err)
		return ret, err
	}

	respBody := resp.Body
	if js, err := simplejson.NewJson(respBody); err == nil {
		if info, isExist := js.CheckGet("info"); isExist { //秒传返回的request_id有可能是科学计数法，这里将它统一转成uint64
			//{"return_type":2,"errno":0,"info":{"size":16877488,"category":4,"fs_id":714504460793248,"request_id":1.821160071156e+17,"path":"\/apps\/\u4e66\u68af\/easy_20210726_163824.pptx","isdir":0,"mtime":1627288705,"ctime":1627288705,"md5":"44090321ds594263c8818d7c398e5017"},"request_id":182116007115598010}
			info.Set("request_id", uint64(info.Get("request_id").MustFloat64()))
			if respBody, err = js.Encode(); err != nil {
				log.Println("simplejson Encode failed, err: ", err)
				return ret, err
			}
		}
	}

	if err := json.Unmarshal(respBody, &ret); err != nil {
		log.Println("json.Unmarshal failed, err: ", err)
		return ret, err
	}

	if ret.ErrorCode != 0 { //错误码不为0
		return ret, errors.New(fmt.Sprintf("error_code:%d, error_msg:%s", ret.ErrorCode, ret.ErrorMsg))
	}

	return ret, nil
}

// 反复上传直到成功或超出重试次数
func (u *Uploader) TrySuperFile2Upload(ctx context.Context, uploadID string, partSeq int, partByte []byte, progressHandler func(int64)) (SuperFile2UploadResponse, error) {
	var partDoneSize int64 = 0
	internalProgressHandler := func(writtenSize int64) {
		partDoneSize += writtenSize
		progressHandler(writtenSize)
	}
	var resp SuperFile2UploadResponse
	var err error
	for i := 0; i < 10; i++ {
		if i > 0 {
			time.Sleep(time.Second * 6)
		}
		resp, err = u.SuperFile2Upload(ctx, uploadID, partSeq, partByte, i, internalProgressHandler)
		if err == nil {
			break
		}
		progressHandler(-partDoneSize)
		partDoneSize = 0
		if ctx.Err() != nil {
			break
		}
	}
	return resp, err
}

// superfile2 upload
func (u *Uploader) SuperFile2Upload(ctx context.Context, uploadID string, partSeq int, partByte []byte, tryIter int, progressHandler func(int64)) (SuperFile2UploadResponse, error) {
	ret := SuperFile2UploadResponse{}

	path := u.Path
	localFilePath := u.LocalFilePath

	// path urlencode
	v := url.Values{}
	v.Add("access_token", u.AccessToken)
	v.Add("path", path)
	v.Add("type", "tmpfile")
	v.Add("uploadid", uploadID)
	v.Add("partseq", strconv.Itoa(partSeq))
	queryParams := v.Encode()
	uploadUrl := conf.PcsDataDomain + Superfile2UploadUri + "&" + queryParams
	fileUploader := fileUtil.NewFileUploader(uploadUrl, localFilePath)
	resp, err := fileUploader.UploadByByte(ctx, partByte, progressHandler)
	if err != nil {
		log.Printf("upload fileUploader.UploadByByte failed tryIter: %d seq: %d path: %s err: %v", tryIter, partSeq, path, err)
		return ret, err
	}

	if err := json.Unmarshal(resp, &ret); err != nil {
		log.Printf("upload json.Unmarshal failed tryIter: %d seq: %d path: %s response: %s err: %v", tryIter, partSeq, path, string(resp), err)
		return ret, err
	}

	if ret.ErrorCode != 0 { //错误码不为0
		log.Printf("upload failed tryIter: %d seq: %d path: %s response: %s", tryIter, partSeq, path, string(resp))
		return ret, errors.New(fmt.Sprintf("error_code:%d, error_msg:%s", ret.ErrorCode, ret.ErrorMsg))
	}

	return ret, nil
}

// file create
func (u *Uploader) Create(ctx context.Context, uploadID string, blockList []string) (UploadResponse, error) {
	ret := UploadResponse{}

	fileInfo, err := u.GetFileInfo(false)
	if err != nil {
		log.Println("GetFileInfo failed, err:", err)
		return ret, err
	}

	blockListByte, err := json.Marshal(blockList)
	if err != nil {
		return ret, err
	}
	blockListStr := string(blockListByte)

	// path urlencode
	v := url.Values{}
	v.Add("path", u.Path)
	v.Add("uploadid", uploadID)
	v.Add("block_list", blockListStr)
	v.Add("size", strconv.FormatInt(fileInfo.Size, 10))
	v.Add("isdir", "0")
	v.Add("rtype", "3") // 3为覆盖
	body := v.Encode()

	requestUrl := conf.OpenApiDomain + CreateUri + "&access_token=" + u.AccessToken

	headers := make(map[string]string)
	resp, err := httpclient.Post(ctx, requestUrl, headers, body)
	if err != nil {
		log.Println("httpclient.Post failed, err:", err)
		return ret, err
	}

	if err := json.Unmarshal(resp.Body, &ret); err != nil {
		log.Printf("json.Unmarshal failed, resp[%s], err[%v]", string(resp.Body), err)
		return ret, err
	}

	if ret.ErrorCode != 0 { //错误码不为0
		log.Println("file create failed, resp:", string(resp.Body))
		return ret, errors.New(fmt.Sprintf("error_code:%d, error_msg:%s", ret.ErrorCode, ret.ErrorMsg))
	}

	return ret, nil
}

// 获取分片的大小
func (u *Uploader) GetSliceSize(fileSize int64) (int64, error) {
	if u.SliceSize > 0 {
		return u.SliceSize, nil
	}

	var sliceSize int64

	/*
		限制：
			普通用户单个分片大小固定为4MB（文件大小如果小于4MB，无需切片，直接上传即可），单文件总大小上限为4G。
			普通会员用户单个分片大小上限为16MB，单文件总大小上限为10G。
			超级会员用户单个分片大小上限为32MB，单文件总大小上限为20G。
	*/
	//切割文件，单个分片大小暂时先固定为4M，TODO 普通会员和超级会员单个分片可以更大，需判断用户的身份
	sliceSize = 4194304 //4M
	accountClient := account.NewAccountClient(u.AccessToken)
	userInfo, err := accountClient.UserInfo()
	if err != nil { //获取失败直接用4M
		log.Println("account.UserInfo failed, err:", err)
		return sliceSize, nil
	}
	if userInfo.VipType == 1 { //普通会员
		sliceSize = 16777216 //16M
	} else if userInfo.VipType == 2 { //超级会员
		sliceSize = 33554432 //32M
	}

	if fileSize <= sliceSize { //无须切片
		sliceSize = fileSize
	}
	u.SliceSize = sliceSize

	return sliceSize, nil
}

// 获取block_list
func (u *Uploader) getBlockList(ctx context.Context, progressHandler func(int64)) ([]string, error) {
	blockList := []string{}
	filePath := u.LocalFilePath
	fileInfo, err := u.GetFileInfo(false)
	if err != nil {
		log.Println("GetFileInfo failed, err:", err)
		return blockList, err
	}
	fileSize := fileInfo.Size
	fileMd5 := fileInfo.Md5

	sliceSize, err := u.GetSliceSize(fileSize)
	if err != nil {
		log.Println("GetSliceSize failed, err:", err)
		return blockList, err
	}

	if sliceSize == fileSize { //只有一个分片
		blockList = append(blockList, fileMd5)
		return blockList, nil
	}

	buffer := make([]byte, sliceSize)
	file, err := os.Open(filePath)
	if err != nil {
		return blockList, err
	}
	defer file.Close()

	for {
		select {
		case <-ctx.Done():
			return blockList, ctx.Err()
		default:
			break
		}
		n, err := file.Read(buffer)
		if err != nil && err != io.EOF {
			log.Println("file.Read failed, err:", err)
			return blockList, err
		}
		if n == 0 {
			break
		}
		hash := md5.New()
		hash.Write(buffer[0:n])
		sliceMd5 := hex.EncodeToString(hash.Sum(nil))
		blockList = append(blockList, sliceMd5)
		progressHandler(int64(n))
	}

	return blockList, nil
}

// 获取文件信息
func (u *Uploader) GetFileInfo(simpleMode bool) (LocalFileInfo, error) {
	if u.FileInfo.Md5 != "" {
		return u.FileInfo, nil
	}
	info := LocalFileInfo{}
	file, err := os.Open(u.LocalFilePath)
	if err != nil {
		return info, err
	}
	defer file.Close()
	fileInfo, err := file.Stat()
	if err != nil {
		return info, err
	}
	info.Size = fileInfo.Size()
	info.ModTime = fileInfo.ModTime().Unix()
	if !simpleMode {
		hash, fileBuf := md5.New(), make([]byte, 1<<20)
		for {
			nr, err := file.Read(fileBuf)
			if nr > 0 {
				io.Copy(hash, bytes.NewReader(fileBuf[:nr]))
				continue
			}
			if err != nil {
				if err == io.EOF {
					break
				}
				log.Println("fileMd5 read file failed, err:", err)
				return info, err
			}
		}
		fileMd5 := hex.EncodeToString(hash.Sum(nil))
		info.Md5 = fileMd5
	}
	u.FileInfo = info
	return info, nil
}

// 特殊字符处理，文件名里有特殊字符时无法上传到网盘，特殊字符有'\\', '?', '|', '"', '>', '<', ':', '*',"\t","\n","\r","\0","\x0B"
func handleSpecialChar(char string) string {
	specialChars := []string{"\\\\", "?", "|", "\"", ">", "<", ":", "*", "\t", "\n", "\r", "\\0", "\\x0B"}

	newChar := char
	for _, specialChar := range specialChars {
		newChar = strings.Replace(newChar, specialChar, "", -1)
	}

	if newChar != char {
		fmt.Printf("char has handle, origin[%s] handled[%s]", char, newChar)
	}

	return newChar
}

// 获取分片的md5值
func (u *Uploader) getSliceMd5() (string, error) {
	var sliceMd5 string
	var sliceSize int64
	sliceSize = 262144 //切割的块大小，固定为256KB

	filePath := u.LocalFilePath
	fileInfo, err := u.GetFileInfo(false)
	if err != nil {
		log.Println("GetFileInfo failed, err:", err)
		return sliceMd5, err
	}

	fileSize := fileInfo.Size
	fileMd5 := fileInfo.Md5

	if fileSize <= sliceSize {
		sliceMd5 = fileMd5
	} else {
		file, err := os.Open(filePath)
		if err != nil {
			return sliceMd5, err
		}
		defer file.Close()

		partBuffer := make([]byte, sliceSize)
		if _, err := file.Read(partBuffer); err == nil {
			hash := md5.New()
			hash.Write(partBuffer)
			sliceMd5 = hex.EncodeToString(hash.Sum(nil))
		}
	}

	return sliceMd5, nil
}
