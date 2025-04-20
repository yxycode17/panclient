package file

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// downloadPartSnapshot 下载分片快照
type DownloadPartSnapshot struct {
	From     int64  `json:"from"`
	To       int64  `json:"to"`
	FilePath string `json:"file_path"`
}

// downloadSnapshot 下载任务快照
type DownloadSnapshot struct {
	FsID        uint64                 `json:"fs_id"`
	FileMd5     string                 `json:"file_md5"`
	SavePath    string                 `json:"save_path"`
	Recoverable bool                   `json:"recoverable"`
	VipType     int                    `json:"vip_type"`
	DoneSize    int64                  `json:"done_size"`
	TotalSize   int64                  `json:"total_size"`
	PartSize    int64                  `json:"part_size"`
	TotalPart   int                    `json:"total_part"`
	DoneParts   []DownloadPartSnapshot `json:"done_parts"`
}

// FileDownloader 文件下载器
type Downloader struct {
	FileSize         int64
	Link             string
	FilePath         string
	TotalPart        int //下载线程
	PartSize         int64
	PartCoroutineNum int //分片下载协程数
}

// filePart 文件分片
type Part struct {
	Index    int    //文件分片的序号
	From     int64  //开始byte
	To       int64  //解决byte
	FilePath string //下载到本地的分片文件路径
}

type DownloadPartResponse struct {
	Part  Part
	Error error
}

// NewFileDownloader .
func NewFileDownloader(downloadLink, filePath string) *Downloader {
	return &Downloader{
		FileSize:         0,
		Link:             downloadLink,
		FilePath:         filePath,
		PartSize:         10485760, // 10M
		PartCoroutineNum: 1,
	}
}

func (d *Downloader) SetTotalPart(totalPart int) {
	d.TotalPart = totalPart
}

func (d *Downloader) SetPartSize(partSize int64) {
	d.PartSize = partSize
}

func (d *Downloader) SetCoroutineNum(partCoroutineNum int) {
	d.PartCoroutineNum = partCoroutineNum
}

func (d *Downloader) ensureDirExist(path string, isDir bool) error {
	dir := ""
	if isDir {
		dir = path
	} else {
		dir = filepath.Dir(path)
	}
	var err error
	for i := 0; i < 3; i++ {
		if i > 0 {
			time.Sleep(time.Second)
		}
		err = os.MkdirAll(dir, os.ModePerm)
		if err == nil {
			break
		}
	}
	return err
}

// Run 开始下载任务
func (d *Downloader) Download(ctx context.Context, tempDir string, snapshot *DownloadSnapshot, progressHandler func(int, int64, int64)) ([]string, error) {
	if err := d.ensureDirExist(tempDir, true); err != nil {
		return []string{}, err
	}

	fileTotalSize := d.FileSize
	if d.TotalPart == 0 || fileTotalSize/d.PartSize < int64(d.TotalPart) { //减少range请求次数
		d.TotalPart = int(math.Ceil(float64(fileTotalSize) / float64(d.PartSize)))
	}
	maxTotalPart := 100
	if d.TotalPart > maxTotalPart { //限制分片数量
		d.TotalPart = maxTotalPart
	}
	log.Printf("download totalPart: %d savePath: %s", d.TotalPart, d.FilePath)

	jobs := make([]Part, d.TotalPart)
	eachSize := fileTotalSize / int64(d.TotalPart)
	snapshot.PartSize = eachSize
	snapshot.TotalPart = d.TotalPart
	snapshot.DoneParts = make([]DownloadPartSnapshot, d.TotalPart)

	for i := range jobs {
		jobs[i].Index = i
		if i == 0 {
			jobs[i].From = 0
		} else {
			jobs[i].From = jobs[i-1].To + 1
		}
		if i < d.TotalPart-1 {
			jobs[i].To = jobs[i].From + eachSize
		} else {
			//the last filePart
			jobs[i].To = fileTotalSize - 1
		}
		snapshot.DoneParts[i].From = jobs[i].From
		snapshot.DoneParts[i].To = jobs[i].To
	}

	delFiles := []string{}
	snapshot.Recoverable = true
	partCoroutineNum := d.PartCoroutineNum
	if len(jobs) < partCoroutineNum {
		partCoroutineNum = len(jobs)
	}
	sem := make(chan int, partCoroutineNum) //限制并发数，以防大文件下载导致占用服务器大量网络宽带和磁盘io
	downloadRespChan := make(chan DownloadPartResponse, d.TotalPart)
	var doneSize int64 = 0
	progressTick := time.Now()
	var progressLock sync.Mutex
	internalProgressHandler := func(partDoneSize int64) {
		progressLock.Lock()
		defer progressLock.Unlock()
		doneSize += partDoneSize
		oldTick := progressTick
		newTick := time.Now()
		if newTick.Sub(oldTick).Milliseconds() >= 500 || doneSize == fileTotalSize {
			progressHandler(2, doneSize, fileTotalSize)
			progressTick = newTick
		}
	}
	hasFailed := false
	var downloadErr error
	downloadPartNum := 0
	for _, job := range jobs {
		if hasFailed {
			break
		}
		select {
		case <-ctx.Done():
			downloadErr = ctx.Err()
		default:
			break
		}
		if downloadErr != nil {
			break
		}
		sem <- 1 //当通道已满的时候将被阻塞
		go func(job Part) {
			part, err := d.tryDownloadPart(ctx, job, tempDir, internalProgressHandler)
			if err != nil {
				log.Printf("download downloader.tryDownloadPart failed savePath: %s part: %v err: %v", d.FilePath, job, err)
				hasFailed = true
			}
			downloadRespChan <- DownloadPartResponse{part, err}
			<-sem
		}(job)
		downloadPartNum++
	}

	doneParts := make([]Part, d.TotalPart)
	for i := 0; i < downloadPartNum; i++ {
		resp := <-downloadRespChan
		if resp.Error != nil {
			if downloadErr == nil {
				downloadErr = resp.Error
			}
			if resp.Part.FilePath != "" {
				delFiles = append(delFiles, resp.Part.FilePath)
			}
			continue
		}
		doneParts[resp.Part.Index] = resp.Part
		snapshot.DoneParts[resp.Part.Index].FilePath = resp.Part.FilePath
		snapshot.DoneSize += (resp.Part.To - resp.Part.From + 1)
	}
	if downloadErr != nil {
		return delFiles, downloadErr
	} else if downloadPartNum != d.TotalPart {
		errStr := fmt.Sprintf("download download part num and total part mismatch partNum: %d expected: %d", downloadPartNum, d.TotalPart)
		log.Println(errStr)
		return delFiles, errors.New(errStr)
	}

	doneSize = 0
	mergeProgressHandler := func(partDoneSize int64) {
		doneSize += partDoneSize
		oldTick := progressTick
		newTick := time.Now()
		if newTick.Sub(oldTick).Milliseconds() >= 500 || doneSize == fileTotalSize {
			progressHandler(3, doneSize, fileTotalSize)
			progressTick = newTick
		}
	}
	downloadErr = d.mergeFileParts(ctx, doneParts, mergeProgressHandler)
	if downloadErr == nil {
		for _, p := range doneParts {
			delFiles = append(delFiles, p.FilePath)
		}
		snapshot.Recoverable = false
	}

	return delFiles, downloadErr
}

// 从断点继续下载
func (d *Downloader) ResumeDownload(ctx context.Context, tempDir string, snapshot *DownloadSnapshot, progressHandler func(int, int64, int64)) ([]string, error) {
	if err := d.ensureDirExist(tempDir, true); err != nil {
		return []string{}, err
	}

	fileTotalSize := snapshot.TotalSize
	d.TotalPart = snapshot.TotalPart
	log.Printf("resumeDownload totalPart: %d savePath: %s", d.TotalPart, d.FilePath)

	delFiles := []string{}
	snapshot.Recoverable = true
	partCoroutineNum := d.PartCoroutineNum
	if d.TotalPart < partCoroutineNum {
		partCoroutineNum = d.TotalPart
	}
	sem := make(chan int, partCoroutineNum) //限制并发数，以防大文件下载导致占用服务器大量网络宽带和磁盘io
	downloadRespChan := make(chan DownloadPartResponse, d.TotalPart)
	doneSize := snapshot.DoneSize
	progressTick := time.Now()
	var progressLock sync.Mutex
	internalProgressHandler := func(partDoneSize int64) {
		progressLock.Lock()
		defer progressLock.Unlock()
		doneSize += partDoneSize
		oldTick := progressTick
		newTick := time.Now()
		if newTick.Sub(oldTick).Milliseconds() >= 500 || doneSize == fileTotalSize {
			progressHandler(2, doneSize, fileTotalSize)
			progressTick = newTick
		}
	}
	hasFailed := false
	var downloadErr error
	downloadPartNum := 0
	donePartNum := 0
	for i, part := range snapshot.DoneParts {
		if part.FilePath == "" {
			continue
		}
		_, err := os.Stat(part.FilePath)
		if err == nil {
			continue
		}
		if !os.IsNotExist(err) {
			delFiles = append(delFiles, part.FilePath)
		}
		snapshot.DoneParts[i].FilePath = ""
		doneSize -= (snapshot.DoneParts[i].To - snapshot.DoneParts[i].From + 1)
		log.Printf("resumeDownload os.Stat failed path: %s err: %v", part.FilePath, err)
	}
	if doneSize < 0 {
		doneSize = 0
	}
	snapshot.DoneSize = doneSize
	for i, part := range snapshot.DoneParts {
		if hasFailed {
			break
		}
		select {
		case <-ctx.Done():
			downloadErr = ctx.Err()
		default:
			break
		}
		if downloadErr != nil {
			break
		}
		if part.FilePath != "" {
			donePartNum++
			continue
		}
		sem <- 1 //当通道已满的时候将被阻塞
		go func(job Part) {
			part, err := d.tryDownloadPart(ctx, job, tempDir, internalProgressHandler)
			if err != nil {
				log.Printf("resumeDownload downloader.tryDownloadPart failed savePath: %s part: %v err: %v", d.FilePath, job, err)
				hasFailed = true
			}
			downloadRespChan <- DownloadPartResponse{part, err}
			<-sem
		}(Part{Index: i, From: part.From, To: part.To})
		downloadPartNum++
		donePartNum++
	}

	for i := 0; i < downloadPartNum; i++ {
		resp := <-downloadRespChan
		if resp.Error != nil {
			if downloadErr == nil {
				downloadErr = resp.Error
			}
			if resp.Part.FilePath != "" {
				delFiles = append(delFiles, resp.Part.FilePath)
			}
			continue
		}
		snapshot.DoneParts[resp.Part.Index].FilePath = resp.Part.FilePath
		snapshot.DoneSize += (resp.Part.To - resp.Part.From + 1)
	}
	if downloadErr != nil {
		return delFiles, downloadErr
	} else if donePartNum != d.TotalPart {
		log.Printf("resumeDownload done part num and total part mismatch donePartNum: %d totalPart: %d", donePartNum, d.TotalPart)
		return delFiles, errors.New("done part num and total part mismatch")
	}

	doneSize = 0
	mergeProgressHandler := func(partDoneSize int64) {
		doneSize += partDoneSize
		oldTick := progressTick
		newTick := time.Now()
		if newTick.Sub(oldTick).Milliseconds() >= 500 || doneSize == fileTotalSize {
			progressHandler(3, doneSize, fileTotalSize)
			progressTick = newTick
		}
	}
	doneParts := make([]Part, d.TotalPart)
	for i, p := range snapshot.DoneParts {
		doneParts[i] = Part{Index: i, From: p.From, To: p.To, FilePath: p.FilePath}
	}
	downloadErr = d.mergeFileParts(ctx, doneParts, mergeProgressHandler)
	if downloadErr == nil {
		for _, p := range doneParts {
			delFiles = append(delFiles, p.FilePath)
		}
		snapshot.Recoverable = false
	}

	return delFiles, downloadErr
}

// 反复获取下载文件信息，直到成功或超出重试次数
func (d *Downloader) TryPrepare(ctx context.Context) (bool, error) {
	var supportRange bool
	var err error
	for i := 0; i < 5; i++ {
		if i > 0 {
			time.Sleep(time.Second)
		}
		supportRange, err = d.Prepare(ctx)
		if err == nil {
			break
		}
	}
	return supportRange, err
}

// prepare 获取要下载的文件的基本信息(header) 使用HTTP Method Head
func (d *Downloader) Prepare(ctx context.Context) (bool, error) {
	isSupportRange := false
	r, err := d.getNewRequestWithContext("HEAD", ctx)
	if err != nil {
		return isSupportRange, err
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return isSupportRange, err
	}
	if resp.StatusCode > 299 {
		return isSupportRange, errors.New(fmt.Sprintf("Can't process, response is %v", resp))
	}
	//检查是否支持 断点续传
	if resp.Header.Get("Accept-Ranges") == "bytes" {
		isSupportRange = true
	}

	//获取文件大小
	contentLength, err := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	if err != nil {
		return isSupportRange, err
	}
	d.FileSize = contentLength

	return isSupportRange, nil
}

// 反复下载分片直到成功或超出重试次数
func (d *Downloader) tryDownloadPart(ctx context.Context, part Part, tempDir string, progressHandler func(int64)) (Part, error) {
	var partDoneSize int64 = 0
	internalProgressHandler := func(readSize int64) {
		partDoneSize += readSize
		progressHandler(readSize)
	}
	var retPart Part
	var err error
	for i := 0; i < 10; i++ {
		if i > 0 {
			time.Sleep(time.Second * 6)
		}
		retPart, err = d.downloadPart(ctx, part, tempDir, i, internalProgressHandler)
		if err == nil {
			break
		}
		if retPart.FilePath != "" {
			os.Remove(retPart.FilePath)
		}
		progressHandler(-partDoneSize)
		partDoneSize = 0
		if ctx.Err() != nil {
			break
		}
	}
	return retPart, err
}

// 下载分片
func (d *Downloader) downloadPart(ctx context.Context, part Part, tempDir string, tryIter int, progressHandler func(int64)) (Part, error) {
	retPart := part
	r, err := d.getNewRequestWithContext("GET", ctx)
	if err != nil {
		return retPart, err
	}
	log.Printf("Downloader.downloadPart 开始[%d]下载 tryIter:%d from:%d to:%d\n", part.Index, tryIter, part.From, part.To)
	r.Header.Set("Range", fmt.Sprintf("bytes=%v-%v", part.From, part.To))
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return retPart, err
	}
	defer resp.Body.Close()

	if resp.StatusCode > 299 {
		buffer, _ := ioutil.ReadAll(resp.Body)
		log.Println(fmt.Sprintf("Downloader.downloadPart 服务器错误 tryIter: %d statusCode: %v, msg:%s", tryIter, resp.StatusCode, string(buffer)))
		return retPart, errors.New(fmt.Sprintf("服务器错误，状态码: %v, msg:%s", resp.StatusCode, string(buffer)))
	}

	//分片文件写入到本地临时目录
	fileName := filepath.Base(d.FilePath)
	fileNamePrefix := fileName[0 : len(fileName)-len(filepath.Ext(d.FilePath))]
	nowTime := time.Now().UnixNano() / 1e6
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	partFilePath := filepath.Join(tempDir, fileNamePrefix+"_"+strconv.Itoa(part.Index)+"_"+strconv.FormatInt(nowTime, 10))

	f, err := os.Create(partFilePath)
	if err != nil {
		log.Println("Downloader.downloadPart open file error :", err)
		return retPart, err
	}
	defer f.Close()
	retPart.FilePath = partFilePath

	buffer := make([]byte, 1024*1024)
	doneSize, err := io.CopyBuffer(f, &ProgressByteReader{resp.Body, progressHandler}, buffer)
	if err != nil && err != io.ErrUnexpectedEOF {
		return retPart, err
	}
	expectedDoneSize := (part.To - part.From + 1)
	if doneSize != expectedDoneSize {
		return retPart, errors.New(fmt.Sprintf("Downloader.downloadPart 下载文件分片长度错误, doneSize:%d expectedDoneSize:%d", doneSize, expectedDoneSize))
	}

	log.Printf("Downloader.downloadPart 结束[%d]下载 tryIter:%d from:%d to:%d\n", part.Index, tryIter, part.From, part.To)
	return retPart, nil
}

// mergeFileParts 合并下载的文件
func (d *Downloader) mergeFileParts(ctx context.Context, parts []Part, progressHandler func(int64)) error {
	log.Println("开始合并文件")

	if err := d.ensureDirExist(d.FilePath, false); err != nil {
		return err
	}

	mergedFile, err := os.Create(d.FilePath)
	if err != nil {
		return err
	}
	defer mergedFile.Close()
	var totalSize int64 = 0
	buffer := make([]byte, 4*1024*1024)
	copyFunc := func(filePath string) error {
		partFile, err := os.Open(filePath)
		if err != nil {
			return err
		}
		defer partFile.Close()
		nw, err := io.CopyBuffer(mergedFile, partFile, buffer)
		if err != nil {
			return err
		}
		totalSize += nw
		progressHandler(nw)
		return nil
	}
	for _, p := range parts {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			break
		}
		err := copyFunc(p.FilePath)
		if err != nil {
			return err
		}
	}
	if totalSize != d.FileSize {
		return errors.New("文件不完整")
	}
	return nil
}

// 直接下载整个文件
func (d *Downloader) DownloadWhole(ctx context.Context, totalSize int64, progressHandler func(int, int64, int64)) error {
	log.Printf("downloadWhole savePath: %s", d.FilePath)

	// Get the data
	r, err := d.getNewRequestWithContext("GET", ctx)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if err := d.ensureDirExist(d.FilePath, false); err != nil {
		return err
	}

	// 创建一个文件用于保存
	f, err := os.Create(d.FilePath)
	if err != nil {
		return err
	}
	defer f.Close()

	buffer := make([]byte, 1024*1024)
	var doneSize int64 = 0
	progressTick := time.Now()
	internalProgressHandler := func(status int, doneSize, totalSize int64) {
		oldTick := progressTick
		newTick := time.Now()
		if newTick.Sub(oldTick).Milliseconds() >= 500 || doneSize == totalSize {
			progressHandler(status, doneSize, totalSize)
			progressTick = newTick
		}
	}
	for {
		nr, err := resp.Body.Read(buffer)
		if nr > 0 {
			nw, err := f.Write(buffer[:nr])
			if err != nil {
				return err
			}
			doneSize += int64(nw)
			internalProgressHandler(2, doneSize, totalSize)
		}
		if err != nil {
			if err != io.EOF && err != io.ErrUnexpectedEOF {
				return err
			}
			break
		}
	}
	return nil
}

// getNewRequest 创建一个request
func (d *Downloader) getNewRequest(method string) (*http.Request, error) {
	r, err := http.NewRequest(
		method,
		d.Link,
		nil,
	)
	if err != nil {
		return nil, err
	}

	r.Header.Set("User-Agent", "pan.baidu.com")
	return r, nil
}

// getNewRequestWithContext 创建一个request
func (d *Downloader) getNewRequestWithContext(method string, ctx context.Context) (*http.Request, error) {
	r, err := http.NewRequestWithContext(
		ctx,
		method,
		d.Link,
		nil,
	)
	if err != nil {
		return nil, err
	}

	r.Header.Set("User-Agent", "pan.baidu.com")
	return r, nil
}
