package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gocolly/colly"
)

type requestInfo struct {
	Page     string
	Token    string
	Callback string
}

type wakeInfo struct {
	Identifier string
}

type statusInfo struct {
	StatusCode int
	IsPlaying  bool
	GameName   string
	GameLink   string
	GameIcon   string
}

type callbackData struct {
	Refresh string
}

type callbackInfo struct {
	Success bool
	Data    callbackData
}

var client http.Client
var statusCache map[string]string
var requestQueue map[string]requestInfo
var requestQueueLock sync.Mutex

func hashInfo(r *requestInfo) string {
	return r.Page + "|" + r.Callback
}

func hashStatus(s *statusInfo, r *requestInfo) string {
	return s.GameLink + "|" + strconv.FormatBool(s.IsPlaying) + "|" + r.Callback
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "Server is online!")
}

func wakeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		decoder := json.NewDecoder(r.Body)

		var body wakeInfo
		err := decoder.Decode(&body)
		if err != nil || len(body.Identifier) == 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		fmt.Fprintf(w, body.Identifier)
		return
	}

	w.WriteHeader(http.StatusBadRequest)
}

func lookupHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		decoder := json.NewDecoder(r.Body)

		var body requestInfo
		err := decoder.Decode(&body)
		if err != nil || len(body.Page) == 0 || len(body.Token) == 0 || len(body.Callback) == 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		_, errOne := url.ParseRequestURI(body.Page)
		_, errTwo := url.ParseRequestURI(body.Callback)
		if errOne != nil || errTwo != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		key := hashInfo(&body)

		requestQueueLock.Lock()
		if _, ok := requestQueue[key]; !ok {
			requestQueue[key] = body
		}
		requestQueueLock.Unlock()

		response, _ := json.Marshal(struct {
			Success bool `json:"success"`
		}{
			true,
		})

		w.Header().Add("Content-Type", "application/json")
		w.Write(response)

		return
	}

	w.WriteHeader(http.StatusBadRequest)
}

func gatherStatus(url string) *statusInfo {
	collector := colly.NewCollector()
	response := &statusInfo{}

	collector.OnHTML(".profile_in_game_header", func(e *colly.HTMLElement) {
		if strings.Contains(e.Text, "In-Game") {
			response.IsPlaying = true
		}
	})

	collector.OnHTML(".recent_games .game_info", func(e *colly.HTMLElement) {
		if len(response.GameName) == 0 {
			response.GameName = e.ChildText(".game_name > a")
			response.GameLink = e.ChildAttr(".game_info_cap > a", "href")
			response.GameIcon = e.ChildAttr(".game_info_cap img", "src")
		}
	})

	collector.OnResponse(func(r *colly.Response) {
		response.StatusCode = r.StatusCode
	})

	collector.OnError(func(r *colly.Response, err error) {
		response.StatusCode = r.StatusCode
	})

	collector.Visit(url)

	return response
}

func runUpdate() {
	for {
		requests := []requestInfo{}

		requestQueueLock.Lock()
		for _, info := range requestQueue {
			requests = append(requests, info)
		}
		requestQueueLock.Unlock()

		for _, info := range requests {
			response := gatherStatus(info.Page)

			if response.StatusCode == 200 {
				key := hashInfo(&info)
				dump := hashStatus(response, &info)

				if item, ok := statusCache[key]; ok && item == dump {
					continue
				}

				statusCache[key] = dump

				form := url.Values{}
				form.Add("page", info.Page)
				form.Add("gameName", response.GameName)
				form.Add("gameLink", response.GameLink)
				form.Add("gameIcon", response.GameIcon)
				form.Add("isPlaying", strconv.FormatBool(response.IsPlaying))

				req, err := http.NewRequest("POST", info.Callback, strings.NewReader(form.Encode()))
				if err != nil {
					delete(statusCache, key)
					continue
				}

				req.Header.Add("API-Route", "Steam")
				req.Header.Add("API-Token", info.Token)
				req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

				callback, err := client.Do(req)
				if err != nil {
					delete(statusCache, key)
					requestQueueLock.Lock()
					delete(requestQueue, key)
					requestQueueLock.Unlock()
					continue
				}

				defer callback.Body.Close()
				body, err := ioutil.ReadAll(callback.Body)
				if err != nil {
					delete(statusCache, key)
					requestQueueLock.Lock()
					delete(requestQueue, key)
					requestQueueLock.Unlock()
					continue
				}

				jsonBody := callbackInfo{}

				if json.Unmarshal(body, &jsonBody) != nil || !jsonBody.Success || len(jsonBody.Data.Refresh) == 0 {
					delete(statusCache, key)
					requestQueueLock.Lock()
					delete(requestQueue, key)
					requestQueueLock.Unlock()
					continue
				}

				requestQueueLock.Lock()
				copy := requestQueue[key]
				copy.Token = jsonBody.Data.Refresh
				requestQueue[key] = copy
				requestQueueLock.Unlock()
			}

			time.Sleep(3000 * time.Millisecond)
		}

		time.Sleep(30000 * time.Millisecond)
	}
}

func main() {
	client = http.Client{}
	statusCache = make(map[string]string)
	requestQueue = make(map[string]requestInfo)

	http.HandleFunc("/", indexHandler)
	http.HandleFunc("/wake", wakeHandler)
	http.HandleFunc("/lookup", lookupHandler)

	go runUpdate()

	log.Println("Server is now running...")
	log.Fatal(http.ListenAndServe(":5555", nil))
}
