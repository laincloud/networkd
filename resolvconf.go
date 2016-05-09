package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/Sirupsen/logrus"
	"github.com/fsnotify/fsnotify"
	"io"
	"io/ioutil"
	"net"
	"strings"
)

const ResolvConfFilename = "/etc/resolv.conf"

var lastHash string

func (self *Server) InitResolvConf() {
	self.resolvConfIsRunning = false
	self.resolvConfStopCh = make(chan struct{})
	if self.resolvConfFlag {
		UpdateResolvConf()
	}
}

func WatchResolvConf(stopWatchCh <-chan struct{}) <-chan int {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}

	eventCh := make(chan int)
	go func() {
		defer close(eventCh)
		defer watcher.Close()
		for {
			select {
			case event := <-watcher.Events:
				log.WithFields(logrus.Fields{
					"name":  event.Name,
					"op":    event.Op,
					"event": event,
				}).Debug("Fsnotify event")
				if event.Op&fsnotify.Write == fsnotify.Write {
					eventCh <- 1
					log.WithFields(logrus.Fields{
						"name": event.Name,
					}).Info("Modified file")
				}
				if event.Op&fsnotify.Remove == fsnotify.Remove {
					err = watcher.Add(ResolvConfFilename)
					if err != nil {
						log.WithFields(logrus.Fields{
							"err": err,
						}).Fatal("Fail to watch file")
					}
					eventCh <- 1
					log.WithFields(logrus.Fields{
						"name": event.Name,
					}).Info("Modified file")
				}
			case err := <-watcher.Errors:
				log.WithFields(logrus.Fields{
					"err": err,
				}).Error("Fsnotify event")
			case <-stopWatchCh:
				return
			}
		}
	}()

	err = watcher.Add(ResolvConfFilename)
	if err != nil {
		log.WithFields(logrus.Fields{
			"err":  err,
			"file": ResolvConfFilename,
		}).Fatal("Fail to watch file")
	}
	return eventCh
}

func UpdateResolvConf() {
	contents, err := ioutil.ReadFile(ResolvConfFilename)
	if err != nil {
		log.WithFields(logrus.Fields{
			"err":  err,
			"file": ResolvConfFilename,
		}).Fatal("Cannot read file")
		return
	}
	reader := bytes.NewReader(contents)
	hash, err := HashData(reader)
	if err != nil {
		log.WithFields(logrus.Fields{
			"err":  err,
			"file": ResolvConfFilename,
		}).Fatal("Cannot hash file")
		return
	}
	log.WithFields(logrus.Fields{
		"hash":     hash,
		"lastHash": lastHash,
		"file":     ResolvConfFilename,
	}).Debug("File hash")
	if lastHash == hash {
		return
	}

	// 1. ensure 127.0.0.1 in the first
	// 2. remove rotate in options
	var updatedContents []byte
	updatedContents = append(updatedContents, "nameserver 127.0.0.1\n"...)
	scanner := bufio.NewScanner(bytes.NewReader(contents))
	for scanner.Scan() {
		var updatedLine string
		line := scanner.Text()
		if len(line) > 0 && (line[0] == ';' || line[0] == '#') {
			// comment.
			updatedLine = fmt.Sprintf("%s\n", line)
			updatedContents = append(updatedContents, updatedLine...)
			continue
		}
		f := strings.Fields(line)
		if len(f) < 1 {
			updatedLine = fmt.Sprintf("%s\n", line)
			updatedContents = append(updatedContents, updatedLine...)
			continue
		}
		switch f[0] {
		case "nameserver": // add one name server
			if len(f) > 1 { // small, but the standard limit
				// One more check: make sure server name is
				// just an IP address.  Otherwise we need DNS
				// to look it up.
				if net.ParseIP(f[1]) != nil {
					if f[1] != "127.0.0.1" {
						updatedLine = fmt.Sprintf("%s\n", line)
					}
				}
			}
		case "options": // magic options
			updatedLine = fmt.Sprintf("%s\n", strings.Replace(line, "rotate", "", -1))
		default:
			updatedLine = fmt.Sprintf("%s\n", line)
		}
		if updatedLine == "" {
			continue
		}
		updatedContents = append(updatedContents, updatedLine...)
	}

	if err := scanner.Err(); err != nil {
		log.WithFields(logrus.Fields{
			"err":  err,
			"file": ResolvConfFilename,
		}).Fatal("Cannot read file")
		return
	}

	err = ioutil.WriteFile(ResolvConfFilename, updatedContents, 0644)
	if err != nil {
		log.WithFields(logrus.Fields{
			"err":  err,
			"file": ResolvConfFilename,
		}).Error("Cannot write file")
	}

	hash, err = HashData(bytes.NewReader(updatedContents))
	if err != nil {
		log.WithFields(logrus.Fields{
			"err":  err,
			"file": ResolvConfFilename,
		}).Error("Cannot hash file")
		return
	}
	lastHash = hash
}

// HashData returns the sha256 sum of src.
func HashData(src io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, src); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func (self *Server) RunResolvConf() {
	if self.resolvConfIsRunning {
		return
	}
	log.Info("Run resolvconf")
	self.resolvConfIsRunning = true
	stopCh := make(chan struct{})
	eventCh := WatchResolvConf(stopCh)
	go func() {
		defer close(stopCh)
		for {
			select {
			case <-eventCh:
				UpdateResolvConf()
			case <-self.resolvConfStopCh:
				stopCh <- struct{}{}
				return
			}
		}
	}()
}

func (self *Server) StopResolvConf() {
	if self.resolvConfIsRunning {
		log.Info("Stop resolvconf")
		self.resolvConfIsRunning = false
		self.resolvConfStopCh <- struct{}{}
	}
}
