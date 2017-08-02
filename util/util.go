package util

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/Sirupsen/logrus"
	lainlet "github.com/laincloud/lainlet/client"
	"golang.org/x/net/context"
)

var iptablesLock sync.Mutex

type watcherCallback func(data interface{})

var log = logrus.New()

func DoCmd(cmdName string, cmdArgs []string) (bytes.Buffer, error) {
	if cmdName == "iptables" {
		iptablesLock.Lock()
		defer iptablesLock.Unlock()
		cmdArgs = append(cmdArgs, "--wait")
	}
	var cmdOut bytes.Buffer
	var cmdErr bytes.Buffer
	cmd := exec.Command(cmdName, cmdArgs...)
	cmd.Stdout = &cmdOut
	cmd.Stderr = &cmdErr
	err := cmd.Run()
	if err != nil {
		log.WithFields(logrus.Fields{
			"cmd":  cmdName,
			"args": cmdArgs,
			"err":  err,
		}).Debug("Fail to run cmd")
		return cmdErr, err
	}
	log.WithFields(logrus.Fields{
		"cmd":  cmdName,
		"args": cmdArgs,
	}).Debug("Success to run cmd")
	return cmdOut, err
}

func ExecCommand(name string, arg ...string) (string, error) {
	if name == "iptables" {
		iptablesLock.Lock()
		defer iptablesLock.Unlock()
	}

	cmd := exec.Command(name, arg...)
	var cmdOut bytes.Buffer
	cmd.Stdout = &cmdOut
	cmd.Stderr = &cmdOut
	err := cmd.Run()
	return cmdOut.String(), err
}

func WatchConfig(log *logrus.Logger, lainlet *lainlet.Client, configKeyPrefix string, watchCh <-chan struct{}, callback watcherCallback) {
	url := fmt.Sprintf("/v2/configwatcher?target=%s&heartbeat=5", configKeyPrefix)
	retryCounter := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for {
		breakWatch := false
		ch, err := lainlet.Watch(url, ctx)
		if err != nil {
			log.WithFields(logrus.Fields{
				"err":          err,
				"retryCounter": retryCounter,
			}).Error("Fail to connect lainlet")
			if retryCounter > 3 {
				time.Sleep(30 * time.Second)
			} else {
				time.Sleep(1 * time.Second)
			}
			retryCounter++
			continue
		}
		retryCounter = 0
		for {
			select {
			case event, ok := <-ch:
				if !ok {
					breakWatch = true
					break
				}
				if event.Id == 0 {
					// lainlet error for etcd down
					if event.Event == "error" {
						log.WithFields(logrus.Fields{
							"id":    event.Id,
							"event": event.Event,
						}).Error("Fail to watch lainlet")
						time.Sleep(5 * time.Second)
					}
					continue
				}
				var addrs interface{}
				if err = json.Unmarshal(event.Data, &addrs); err == nil {
					callback(addrs)
				} else {
					log.Warnf("Fetch Lainlet data failed with err:%v", err)
				}
			case <-watchCh:
				return
			}
			if breakWatch {
				break
			}
		}
		log.Error("Fail to watch lainlet")
	}
}
