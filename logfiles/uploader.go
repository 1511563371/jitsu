package logfiles

import (
	"github.com/ksensehq/eventnative/appstatus"
	"github.com/ksensehq/eventnative/destinations"
	"github.com/ksensehq/eventnative/logging"
	"github.com/ksensehq/eventnative/metrics"
	"github.com/ksensehq/eventnative/safego"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"time"
)

//PeriodicUploader read already rotated and closed log files
//Pass them to storages according to tokens
//Keep uploading log file with result statuses
type PeriodicUploader struct {
	logEventPath string
	fileMask     string
	uploadEvery  time.Duration

	statusManager      *statusManager
	destinationService *destinations.Service
}

func NewUploader(logEventPath, fileMask string, uploadEveryS int, destinationService *destinations.Service) (*PeriodicUploader, error) {
	statusManager, err := newStatusManager(logEventPath)
	if err != nil {
		return nil, err
	}
	return &PeriodicUploader{
		logEventPath:       logEventPath,
		fileMask:           path.Join(logEventPath, fileMask),
		uploadEvery:        time.Duration(uploadEveryS) * time.Second,
		statusManager:      statusManager,
		destinationService: destinationService,
	}, nil
}

//Start reading event logger log directory and finding already rotated and closed files by mask
//pass them to storages according to tokens
//keep uploading log statuses file for every event log file
func (u *PeriodicUploader) Start() {
	safego.RunWithRestart(func() {
		for {
			if appstatus.Instance.Idle {
				break
			}

			if destinations.StatusInstance.Reloading {
				time.Sleep(2 * time.Second)
				continue
			}

			files, err := filepath.Glob(u.fileMask)
			if err != nil {
				logging.Error("Error finding files by mask", u.fileMask, err)
				return
			}

			for _, filePath := range files {
				fileName := filepath.Base(filePath)

				b, err := ioutil.ReadFile(filePath)
				if err != nil {
					logging.Error("Error reading file", filePath, err)
					continue
				}
				if len(b) == 0 {
					os.Remove(filePath)
					continue
				}
				//get token from filename
				regexResult := logging.TokenIdExtractRegexp.FindStringSubmatch(fileName)
				if len(regexResult) != 2 {
					logging.Errorf("Error processing file %s. Malformed name", filePath)
					continue
				}

				tokenId := regexResult[1]
				storageProxies := u.destinationService.GetStorages(tokenId)
				if len(storageProxies) == 0 {
					logging.Warnf("Destination storages weren't found for file [%s] and token [%s]", filePath, tokenId)
					continue
				}

				//flag for deleting file if all storages don't have errors while storing this file
				deleteFile := true
				for _, storageProxy := range storageProxies {
					storage, ok := storageProxy.Get()
					if !ok {
						deleteFile = false
						continue
					}
					if !u.statusManager.isUploaded(fileName, storage.Name()) {
						rowsCount, err := storage.Store(fileName, b)
						if err != nil {
							deleteFile = false
							logging.Errorf("[%s] Error storing file %s in destination: %v", storage.Name(), filePath, err)
							metrics.ErrorTokenEvents(tokenId, storage.Name(), rowsCount)
						} else {
							metrics.SuccessTokenEvents(tokenId, storage.Name(), rowsCount)
						}
						u.statusManager.updateStatus(fileName, storage.Name(), err)
					}
				}

				if deleteFile {
					err := os.Remove(filePath)
					if err != nil {
						logging.Error("Error deleting file", filePath, err)
					} else {
						u.statusManager.cleanUp(fileName)
					}
				}
			}

			time.Sleep(u.uploadEvery)
		}
	})
}
