package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

func replaceEmpty(str, replaceStr string) string {
	if strings.TrimSpace(str) == "" {
		return replaceStr
	}
	return str
}

func renderJSON(rw http.ResponseWriter, r *http.Request, data ResponseData) {
	b, _ := json.Marshal(data)
	rw.Header().Add("Content-Type", "application/json")
	rw.Header().Add("Access-Control-Allow-Methods", "GET,POST,PUT,OPTIONS")
	io.WriteString(rw, string(b))
}

func date(t int64) string {
	if t == 0 {
		return "0"
	}

	return time.Unix(t, 0).Format("2006-01-02 15:04:05")
}

func int2floatstr(f string, n int64, l int) string {
	return fmt.Sprintf(f, float64(n)/float64(l))
}

func getHost(addr string) string {
	sli := strings.Split(addr, ":")
	return sli[0]
}
func getHostPort(addr string) string {
	sli := strings.Split(addr, ":")
	return sli[1]
}

func getHttpClientIp(r *http.Request) string {
	if r.Header.Get("x-forwarded-for") == "" {
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err != nil {
			return ""
		} else {
			return host
		}

	}
	return r.Header.Get("x-forwarded-for")
}
