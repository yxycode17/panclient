package file

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"

	"github.com/jsyzchen/pan/utils/httpclient"
)

type UploadSnapshot struct {
	Path        string   `json:"path"`
	LocalPath   string   `json:"local_path"`
	UploadId    string   `json:"upload_id"`
	FileMd5     string   `json:"file_md5"`
	FileModTime int64    `json:"file_mtime"`
	Recoverable bool     `json:"recoverable"`
	DoneSize    int64    `json:"done_size"`
	TotalSize   int64    `json:"total_size"`
	SliceSize   int64    `json:"slice_size"`
	SliceNum    int      `json:"slice_num"`
	DoneSlices  []string `json:"done_slices"`
}

type Uploader struct {
	Url      string
	FilePath string
}

// NewFileUploader
func NewFileUploader(url, filePath string) *Uploader {
	return &Uploader{
		Url:      url,
		FilePath: filePath,
	}
}

// 上传文件
func (u *Uploader) Upload() ([]byte, error) {
	ret := []byte("")

	bodyBuf := &bytes.Buffer{}
	bodyWriter := multipart.NewWriter(bodyBuf)
	//"file" 为接收时定义的参数名
	fileWriter, err := bodyWriter.CreateFormFile("file", filepath.Base(u.FilePath))
	if err != nil {
		log.Println("error writing to buffer, err:", err)
		return ret, err
	}

	//打开文件
	fh, err := os.Open(u.FilePath)
	if err != nil {
		log.Println("error opening file, err:", err)
		return ret, err
	}
	defer fh.Close()

	//iocopy
	_, err = io.Copy(fileWriter, fh)
	if err != nil {
		return ret, err
	}
	contentType := bodyWriter.FormDataContentType()
	bodyWriter.Close()

	//提交请求
	request, err := http.NewRequest("POST", u.Url, bodyBuf)
	if err != nil {
		return ret, err
	}

	request.Header.Add("Content-Type", contentType)
	//随机设置一个User-Agent
	userAgent := httpclient.GetRandomUserAgent()
	request.Header.Set("User-Agent", userAgent)

	//处理返回结果
	client := &http.Client{}
	resp, err := client.Do(request)
	//打印接口返回信息
	if err != nil {
		log.Println("request uploadUrl failed, err:", err)
		return ret, err
	}
	defer resp.Body.Close()

	respBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return ret, err
	}
	//根据实际需要，返回相应的信息
	return respBody, nil
}

type ProgressByteReader struct {
	io.Reader
	Reporter func(int64)
}

func (pbr *ProgressByteReader) Read(p []byte) (nr int, err error) {
	nr, err = pbr.Reader.Read(p)
	if nr > 0 && pbr.Reporter != nil {
		pbr.Reporter(int64(nr))
	}
	return
}

// 直接通过字节上传
func (u *Uploader) UploadByByte(ctx context.Context, fileByte []byte, progressHandler func(int64)) ([]byte, error) {
	ret := []byte("")
	bodyBuf := &bytes.Buffer{}
	bodyWriter := multipart.NewWriter(bodyBuf)
	//"file" 为接收时定义的参数名
	fileWriter, err := bodyWriter.CreateFormFile("file", filepath.Base(u.FilePath))
	if err != nil {
		return ret, err
	}

	_, err = io.Copy(fileWriter, bytes.NewReader(fileByte))
	if err != nil {
		return ret, err
	}
	contentType := bodyWriter.FormDataContentType()
	bodyWriter.Close()
	contentLength := bodyBuf.Len()

	//提交请求
	request, err := http.NewRequestWithContext(ctx, "POST", u.Url, &ProgressByteReader{bodyBuf, progressHandler})
	if err != nil {
		return ret, err
	}

	request.Header.Add("Content-Type", contentType)
	//随机设置一个User-Agent
	userAgent := httpclient.GetRandomUserAgent()
	request.Header.Set("User-Agent", userAgent)
	request.ContentLength = int64(contentLength)

	//处理返回结果
	client := &http.Client{}
	resp, err := client.Do(request)
	if err != nil {
		return ret, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return ret, errors.New(fmt.Sprintf("http error status: %d msg: %s", resp.StatusCode, resp.Status))
	}

	respBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return ret, err
	}

	return respBody, nil
}
