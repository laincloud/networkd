package server

import (
	"net"
	"regexp"
	"strings"
	"fmt"
	"strconv"
	"github.com/deckarep/golang-set"
)

func isDomain(domain string) bool {
	if isIP(domain) {
		return false
	}
	match, _ := regexp.MatchString(`^([a-zA-Z0-9\*]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,6}$`, domain)
	return match
}

func isIP(ip string) bool {
	return net.ParseIP(ip) != nil
}

// parse the address in server file
// format: ip#port, eg 127.0.0.1#53
func parseServerAddr(addr string) (ip, port string, err error) {
	parts := strings.Split(addr, "#")
	if len(parts) > 2 {
		err = fmt.Errorf("Error: %s is invalid server address.", addr)
		return
	}

	ip = parts[0]
	if !isIP(ip) {
		err = fmt.Errorf("Error: %s is not a ip.", ip)
		return
	}
	port = "53"
	if len(parts) == 2 {
		if _, err := strconv.Atoi(parts[1]); err != nil {
			return ip, port, err
		}
		port = parts[1]
	}
	return
}
func setToStringSlice(s mapset.Set) []string {
	var v []string
	for ele := range s.Iter() {
		v = append(v, ele.(string))
	}
	return v
}
