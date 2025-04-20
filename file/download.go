package file

import (
	"context"
	"errors"
	"log"
	"os"
	"sync"

	"github.com/jsyzchen/pan/account"
	"github.com/jsyzchen/pan/utils/file"
)

type DownloadProgressHandler = func(int, int64, int64)

type Downloader struct {
	LocalFilePath string
	FsID          uint64
	AccessToken   string
	TotalPart     int
}

const (
	PcsFileDownloadUri = "/rest/2.0/pcs/file?method=download"
)

func NewDownloaderWithFsID(accessToken string, fsID uint64, localFilePath string) *Downloader {
	return &Downloader{
		AccessToken:   accessToken,
		FsID:          fsID,
		LocalFilePath: localFilePath,
	}
}

// 获取下载地址
func (d *Downloader) GetDownloadLinkInfo() (string, string, error) {
	if d.FsID == 0 {
		return "", "", errors.New("getDownloadLinkInfo invalid fsid")
	}
	downloadLink := ""
	fileMd5 := ""
	fileClient := NewFileClient(d.AccessToken)
	metas, err := fileClient.Metas([]uint64{d.FsID})
	if err != nil {
		log.Println("getDownloadLinkInfo fileClient.Metas failed err:", err)
		return "", "", err
	}
	if len(metas.List) == 0 {
		log.Println("getDownloadLinkInfo file doesn't exist")
		return "", "", errors.New("getDownloadLinkInfo file doesn't exist")
	}
	downloadLink = metas.List[0].DLink
	fileMd5 = metas.List[0].Md5
	downloadLink += "&access_token=" + d.AccessToken
	return downloadLink, fileMd5, nil
}

// 执行下载
func (d *Downloader) Download(ctx context.Context, tempDir string, progressHandler DownloadProgressHandler) (file.DownloadSnapshot, error) {
	retSnapshot := file.DownloadSnapshot{}
	retSnapshot.FsID = d.FsID
	retSnapshot.SavePath = d.LocalFilePath

	if d.LocalFilePath == "" || d.AccessToken == "" {
		return retSnapshot, errors.New("download local file path or access token is empty")
	}

	downloadLink, fileMd5, err := d.GetDownloadLinkInfo()
	if err != nil {
		return retSnapshot, err
	}
	retSnapshot.FileMd5 = fileMd5

	downloader := file.NewFileDownloader(downloadLink, d.LocalFilePath)
	accountClient := account.NewAccountClient(d.AccessToken)
	if userInfo, err := accountClient.UserInfo(); err == nil {
		log.Println("download VipType:", userInfo.VipType)
		retSnapshot.VipType = userInfo.VipType
		if userInfo.VipType == 2 { //当前用户是超级会员
			downloader.SetPartSize(52428800) //设置每分片下载文件大小，50M
			downloader.SetCoroutineNum(5)    //分片下载并发数，普通用户不支持并发分片下载
		}
	}

	supportRange, err := downloader.TryPrepare(ctx)
	if err != nil {
		log.Printf("download downloader.TryPrepare failed err: %v savePath: %s", err, d.LocalFilePath)
		return retSnapshot, err
	}
	retSnapshot.TotalSize = downloader.FileSize

	if !supportRange || downloader.FileSize <= downloader.PartSize {
		retSnapshot.PartSize = downloader.FileSize
		retSnapshot.TotalPart = 1
		err := downloader.DownloadWhole(ctx, downloader.FileSize, progressHandler)
		if err == nil {
			retSnapshot.DoneSize = downloader.FileSize
		} else {
			log.Printf("download downloader.DownloadWhole failed err: %v savePath: %s", err, d.LocalFilePath)
		}
		return retSnapshot, err
	}

	delFiles, err := downloader.Download(ctx, tempDir, &retSnapshot, progressHandler)
	defer d.RemovePartFiles(delFiles)
	if err != nil {
		log.Printf("download downloader.Download failed err: %v savePath: %s", err, d.LocalFilePath)
		return retSnapshot, err
	}

	return retSnapshot, nil
}

// 从断点继续下载
func (d *Downloader) ResumeDownload(ctx context.Context, snapshot file.DownloadSnapshot, tempDir string, progressHandler DownloadProgressHandler) (file.DownloadSnapshot, error) {
	retSnapshot := snapshot
	retSnapshot.DoneParts = make([]file.DownloadPartSnapshot, snapshot.TotalPart)
	copy(retSnapshot.DoneParts, snapshot.DoneParts)

	if d.LocalFilePath == "" || d.AccessToken == "" {
		return retSnapshot, errors.New("resumeDownload local file path or access token is empty")
	}

	downloadLink, fileMd5, err := d.GetDownloadLinkInfo()
	if err != nil {
		return retSnapshot, err
	}

	downloader := file.NewFileDownloader(downloadLink, d.LocalFilePath)
	accountClient := account.NewAccountClient(d.AccessToken)
	vipType := retSnapshot.VipType
	if userInfo, err := accountClient.UserInfo(); err == nil {
		log.Println("resumeDownload VipType:", userInfo.VipType)
		vipType = userInfo.VipType
	} else {
		vipType = 0
	}
	if vipType == 2 { //当前用户是超级会员
		downloader.SetPartSize(52428800) //设置每分片下载文件大小，50M
		downloader.SetCoroutineNum(5)    //分片下载并发数，普通用户不支持并发分片下载
	}

	supportRange, err := downloader.TryPrepare(ctx)
	if err != nil {
		log.Printf("resumeDownload downloader.TryPrepare failed err: %v savePath: %s", err, d.LocalFilePath)
		return retSnapshot, err
	}

	delFiles := []string{}
	filesFromSnapshot := func(parts []file.DownloadPartSnapshot) {
		for _, p := range parts {
			if p.FilePath != "" {
				delFiles = append(delFiles, p.FilePath)
			}
		}
	}
	defer func() {
		d.RemovePartFiles(delFiles)
	}()

	if !supportRange || downloader.FileSize <= downloader.PartSize {
		retSnapshot.VipType = vipType
		retSnapshot.FileMd5 = fileMd5
		retSnapshot.Recoverable = false
		retSnapshot.DoneSize = 0
		retSnapshot.TotalSize = downloader.FileSize
		retSnapshot.PartSize = downloader.FileSize
		retSnapshot.TotalPart = 1
		filesFromSnapshot(retSnapshot.DoneParts)
		retSnapshot.DoneParts = nil
		err := downloader.DownloadWhole(ctx, downloader.FileSize, progressHandler)
		if err == nil {
			retSnapshot.DoneSize = downloader.FileSize
		} else {
			log.Printf("resumeDownload downloader.DownloadWhole failed err: %v savePath: %s", err, d.LocalFilePath)
		}
		return retSnapshot, err
	}

	if vipType != retSnapshot.VipType || fileMd5 != retSnapshot.FileMd5 {
		log.Printf("resumeDownload vip type or file md5 mismatch, revert to download savePath: %s", d.LocalFilePath)
		retSnapshot.VipType = vipType
		retSnapshot.FileMd5 = fileMd5
		retSnapshot.Recoverable = false
		retSnapshot.DoneSize = 0
		retSnapshot.TotalSize = downloader.FileSize
		retSnapshot.PartSize = 0
		retSnapshot.TotalPart = 0
		filesFromSnapshot(retSnapshot.DoneParts)
		retSnapshot.DoneParts = nil
		files, err := downloader.Download(ctx, tempDir, &retSnapshot, progressHandler)
		delFiles = append(delFiles, files...)
		if err != nil {
			log.Printf("resumeDownload downloader.Download failed err: %v savePath: %s", err, d.LocalFilePath)
			return retSnapshot, err
		}
	} else {
		files, err := downloader.ResumeDownload(ctx, tempDir, &retSnapshot, progressHandler)
		delFiles = append(delFiles, files...)
		if err != nil {
			log.Printf("resumeDownload downloader.ResumeDownload failed err: %v savePath: %s", err, d.LocalFilePath)
			return retSnapshot, err
		}
	}

	return retSnapshot, nil
}

// 删除临时文件
func (d *Downloader) RemovePartFiles(files []string) {
	var wg sync.WaitGroup
	for _, f := range files {
		if f != "" {
			wg.Add(1)
			go func(filePath string) {
				defer wg.Done()
				if err := os.Remove(filePath); err != nil {
					log.Println(filePath, "remove failed, err:", err)
				}
			}(f)
		}
	}
	wg.Wait()
}
