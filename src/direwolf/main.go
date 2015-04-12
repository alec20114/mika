package main

import (
	"bytes"
	"errors"
	"flag"
	"github.com/garyburd/redigo/redis"
	"github.com/jackpal/bencode-go"
	"github.com/labstack/echo"
	mw "github.com/labstack/echo/middleware"
	"github.com/thoas/stats"
	"log"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"
	"fmt"
)

type (
	ScrapeResponse struct{}

	ErrorResponse struct {
		FailReason string `bencode:"failure reason"`
	}

	AnnounceResponse struct {
		FailReason  string `bencode:"failure reason"`
		WarnReason  string `bencode:"warning message"`
		MinInterval int    `bencode:"min interval"`
		Complete    int    `bencode:"complete"`
		Incomplete  int    `bencode:"incomplete"`
		Interval    int    `bencode:"interval"`
		Peers       []int  `bencode:"peers"`
	}

	// Peers
	Peer struct {
		SpeedUP       float64 `redis:"speed_up"`
		SpeedDN       float64 `redis:"speed_dj"`
		Uploaded      uint64  `redis:"uploaded"`
		Downloaded    uint64  `redis:"downloaded"`
		IP            string  `redis:"ip"`
		Port          uint64  `redis:"port"`
		Left          uint64  `redis:"left"`
		Announces     string  `redis:"announces"`
		TotalTime     uint64  `redis:"total_time"`
		AnnounceLast  uint64  `redis:"last_announce"`
		AnnounceFirst uint64  `redis:"first_announce"`
		New           bool    `redis:"new"`
		Active        bool    `redis:"active"`
		UserID        uint64  `redis:"user_id"`
	}

	AnnounceRequest struct {
		Compact    bool   `json:"compact"`
		Downloaded uint64 `json:"downloaded"`
		Event      string `json:"event"`
		IPv4       net.IP `json:"ipv4"`
		Infohash   string `json:"infohash"`
		Left       uint64 `json:"left"`
		NumWant    int    `json:"numwant"`
		Passkey    string `json:"passkey"`
		PeerID     string `json:"peer_id"`
		Port       uint64 `json:"port"`
		Uploaded   uint64 `json:"uploaded"`
	}

	ScrapeRequest struct {
		Passkey    string
		Infohashes []string
	}

	Query struct {
		Infohashes []string
		Params     map[string]string
	}
)

const (
	MSG_OK                      int = 0
	MSG_INVALID_REQ_TYPE        int = 100
	MSG_MISSING_INFO_HASH       int = 101
	MSG_MISSING_PEER_ID         int = 102
	MSG_MISSING_PORT            int = 103
	MSG_INVALID_PORT            int = 104
	MSG_INVALID_INFO_HASH       int = 150
	MSG_INVALID_PEER_ID         int = 151
	MSG_INVALID_NUM_WANT        int = 152
	MSG_INFO_HASH_NOT_FOUND     int = 200
	MSG_CLIENT_REQUEST_TOO_FAST int = 500
	MSG_MALFORMED_REQUEST       int = 901
	MSG_GENERIC_ERROR           int = 900
)

var (

	// Error code to message mappings
	resp_msg = map[int]string{
		MSG_INVALID_REQ_TYPE:        "Invalid request type",
		MSG_MISSING_INFO_HASH:       "info_hash missing from request",
		MSG_MISSING_PEER_ID:         "peer_id missing from request",
		MSG_MISSING_PORT:            "port missing from request",
		MSG_INVALID_PORT:            "Invalid port",
		MSG_INVALID_INFO_HASH:       "Torrent info hash must be 20 characters",
		MSG_INVALID_PEER_ID:         "Peer ID Invalid",
		MSG_INVALID_NUM_WANT:        "num_want invalid",
		MSG_INFO_HASH_NOT_FOUND:     "info_hash was not found, better luck next time",
		MSG_CLIENT_REQUEST_TOO_FAST: "Slot down there jimmy.",
		MSG_MALFORMED_REQUEST:       "Malformed request",
		MSG_GENERIC_ERROR:           "Generic Error :(",
	}
	pool *redis.Pool

	listen_host = flag.String("listen", ":34000", "Host/port to bind to")
	redis_host  = flag.String("redis", "localhost:6379", "Redis endpoint")
	redis_pass  = flag.String("rpass", "", "Redis pasword")
	max_idle    = flag.Int("max_idle", 50, "Max idle redis connections")
	num_procs   = flag.Int("procs", runtime.NumCPU(), "Number of CPU cores to use (default: all)")
	debug       = flag.Bool("debug", false, "Enable debugging output")
)

func GetTorrentID(r redis.Conn, info_hash string) uint64 {
	torrent_id_reply, err := r.Do("GET", fmt.Sprintf("t:info_hash:%x", info_hash))
	if err != nil {
		log.Println("Failed to execute torrent_id query", err)
		return 0
	}
	if torrent_id_reply == nil {
		log.Println("Invalid info hash")
		return 0
	}
	torrent_id, tid_err := redis.Uint64(torrent_id_reply, nil)
	if tid_err != nil {
		log.Println("Failed to parse torrent_id reply", tid_err)
		return 0
	}
	return torrent_id
}

func GetTorrent(r redis.Conn, torrent_id uint64) map[string]string {
	torrent_reply, err := r.Do("HGETALL", fmt.Sprintf("t:t:%d", torrent_id))
	if err != nil {
		log.Println("Error executing torrent fetch query: ", err)
	}
	if torrent_reply == nil {
		log.Println("Making new torrent struct")
		torrent := make(map[string]string)
		return torrent
	} else {
		torrent, err := redis.StringMap(torrent_reply, nil)
		if err != nil {
			log.Println("Failed to fetch torrent: ", err)
		}
		return torrent
	}
}

func GetUserID(r redis.Conn, passkey string) uint64 {
	log.Println("Fetching passkey", passkey)
	user_id_reply, err := r.Do("GET", fmt.Sprintf("t:user:%s", passkey))
	if err != nil {
		log.Println(err)
		return 0
	}
	user_id, errb := redis.Uint64(user_id_reply, nil)
	if errb != nil {
		log.Println("Failed to find user", errb)
		return 0
	}
	return user_id
}

func IsValidClient(r redis.Conn, peer_id string) bool {
	a, err := r.Do("HKEYS", "t:whitelist")

	if err != nil {
		log.Println(err)
		return false
	}
	clients, err := redis.Strings(a, nil)
	for _, client_prefix := range clients {
		if strings.HasPrefix(peer_id, client_prefix) {
			return true
		}
	}
	return false
}

func GetPeers(r *redis.Conn, torrent_id uint) {
	//"t:t:{}:peers"
	// hgetall("t:t:{}:{}
}

func GetPeer(r redis.Conn, torrent_id uint64, peer_id string) map[string]string {
	peer_reply, err := r.Do("HGETALL", fmt.Sprintf("t:t:%d:%s", torrent_id, peer_id))
	if err != nil {
		log.Println("Error executing peer fetch query: ", err)
	}
	if peer_reply == nil {
		log.Println("Making new peer struct")
		peer := make(map[string]string)
		return peer
	} else {
		peer, err := redis.StringMap(peer_reply, nil)
		if err != nil {
			log.Println("Failed to fetch peer: ", err)
		}
		return peer
	}
}

func GentlyTouchPeer(r *redis.Conn, torrent_id uint, peer_id string) {

}

// New parses a raw url query.
func QueryStringParser(query string) (*Query, error) {
	var (
		keyStart, keyEnd int
		valStart, valEnd int
		firstInfohash    string

		onKey       = true
		hasInfohash = false

		q = &Query{
			Infohashes: nil,
			Params:     make(map[string]string),
		}
	)

	for i, length := 0, len(query); i < length; i++ {
		separator := query[i] == '&' || query[i] == ';' || query[i] == '?'
		if separator || i == length-1 {
			if onKey {
				keyStart = i + 1
				continue
			}
			if i == length-1 && !separator {
				if query[i] == '=' {
					continue
				}
				valEnd = i
			}
			keyStr, err := url.QueryUnescape(query[keyStart : keyEnd+1])
			if err != nil {
				return nil, err
			}
			valStr, err := url.QueryUnescape(query[valStart : valEnd+1])
			if err != nil {
				return nil, err
			}
			q.Params[strings.ToLower(keyStr)] = valStr

			if keyStr == "info_hash" {
				if hasInfohash {
					// Multiple infohashes
					if q.Infohashes == nil {
						q.Infohashes = []string{firstInfohash}
					}
					q.Infohashes = append(q.Infohashes, valStr)
				} else {
					firstInfohash = valStr
					hasInfohash = true
				}
			}
			onKey = true
			keyStart = i + 1
		} else if query[i] == '=' {
			onKey = false
			valStart = i + 1
		} else if onKey {
			keyEnd = i
		} else {
			valEnd = i
		}
	}

	return q, nil
}

// Uint64 is a helper to obtain a uint of any length from a Query. After being
// called, you can safely cast the uint64 to your desired length.
func (q *Query) Uint64(key string) (uint64, error) {
	str, exists := q.Params[key]
	if !exists {
		return 0, errors.New("value does not exist for key: " + key)
	}

	val, err := strconv.ParseUint(str, 10, 64)
	if err != nil {
		return 0, err
	}
	return val, nil
}

func UMax(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}

func NewAnnounce(c *echo.Context) (*AnnounceRequest, error) {
	log.Println(c.Request.RequestURI)
	q, err := QueryStringParser(c.Request.RequestURI)
	if err != nil {
		return nil, err
	}

	s := strings.Split(c.Request.RemoteAddr, ":")
	ip_req, _ := s[0], s[1]

	compact := q.Params["compact"] != "0"
	event, _ := q.Params["event"]

	numWant := getNumWant(q, 30)

	infohash, exists := q.Params["info_hash"]
	if !exists {
		return nil, errors.New("Invalid info hash")
	}

	peerID, exists := q.Params["peer_id"]
	if !exists {
		return nil, errors.New("Invalid peer_id")
	}

	ipv4, err := getIP(q.Params["ip"])
	if err != nil {
		ipv4_new, err := getIP(ip_req)
		if err != nil {
			log.Println(err)
			return nil, errors.New("Invalid ip hash")
		}
		ipv4 = ipv4_new
	}

	port, err := q.Uint64("port")
	if err != nil || port < 1024 || port > 65535 {
		return nil, errors.New("Invalid port")
	}

	left, err := q.Uint64("left")
	if err != nil {
		return nil, errors.New("No left value")
	} else {
		left = UMax(0, left)
	}

	downloaded, err := q.Uint64("downloaded")
	if err != nil {
		return nil, errors.New("Invalid downloaded value")
	} else {
		downloaded = UMax(0, downloaded)
	}

	uploaded, err := q.Uint64("uploaded")
	if err != nil {
		return nil, errors.New("Invalid uploaded value")
	} else {
		uploaded = UMax(0, uploaded)
	}

	return &AnnounceRequest{
		Compact:    compact,
		Downloaded: downloaded,
		Event:      event,
		IPv4:       ipv4,
		Infohash:   infohash,
		Left:       left,
		NumWant:    numWant,
		PeerID:     peerID,
		Port:       port,
		Uploaded:   uploaded,
	}, nil
}

func getIP(ip_str string) (net.IP, error) {

	ip := net.ParseIP(ip_str)
	if ip != nil {
		return ip.To4(), nil
	}
	return nil, errors.New("Failed to parse ip")
}

func getNumWant(q *Query, fallback int) int {
	if numWantStr, exists := q.Params["numwant"]; exists {
		numWant, err := strconv.Atoi(numWantStr)
		if err != nil {
			return fallback
		}
		return numWant
	}

	return fallback
}

func newPool(server, password string, max_idle int) *redis.Pool {
	return &redis.Pool{
		MaxIdle:     max_idle,
		IdleTimeout: 240 * time.Second,
		Dial: func() (redis.Conn, error) {
			c, err := redis.Dial("tcp", server)
			if err != nil {
				return nil, err
			}
			if password != "" {
				if _, err := c.Do("AUTH", password); err != nil {
					c.Close()
					return nil, err
				}
			}
			return c, err
		},
	}
}

func oops(c *echo.Context, msg_code int) {
	c.String(msg_code, responseError(resp_msg[msg_code]))
}

func responseError(message string) string {
	var out_bytes bytes.Buffer
	var er_msg = ErrorResponse{FailReason: message}
	er_msg_encoded := bencode.Marshal(&out_bytes, er_msg)
	if er_msg_encoded == nil {
		return "error"
	}
	return out_bytes.String()
}

func handleAnnounce(c *echo.Context) {

	r := pool.Get()
	defer r.Close()

	ann, err := NewAnnounce(c)

	if err != nil {
		log.Fatalln(err)
		oops(c, MSG_GENERIC_ERROR)
		return
	}

	passkey := c.Param("passkey")

	var user_id = GetUserID(r, passkey)
	if user_id <= 0 {
		oops(c, MSG_GENERIC_ERROR)
		return
	}

	log.Println(fmt.Sprint("UserID: ", user_id))

	if !IsValidClient(r, ann.PeerID) {
		oops(c, MSG_INVALID_PEER_ID)
		return
	}

	var torrent_id = GetTorrentID(r, ann.Infohash)
	if torrent_id <= 0 {
		oops(c, MSG_INFO_HASH_NOT_FOUND)
		return
	}
	log.Println(fmt.Sprint("TorrentID: ", torrent_id))

	peer := GetPeer(r, torrent_id, ann.PeerID)
	log.Println(peer)

	torrent := GetTorrent(r, torrent_id)
	log.Println(torrent)

	resp := responseError("hello!")
	log.Println(resp)
	c.String(http.StatusOK, resp)
}

func handleScrape(c *echo.Context) {
	c.String(http.StatusOK, "I like to scrape my ass")
}

func main() {
	// Parse CLI args
	flag.Parse()

	// Set max number of CPU cores to use
	log.Println("Num procs(s):", *num_procs)
	runtime.GOMAXPROCS(*num_procs)

	// Initialize the redis pool
	pool = newPool(*redis_host, *redis_pass, *max_idle)

	// Initialize the router + middlewares
	e := echo.New()
	e.MaxParam(1)

	if *debug {
		e.Use(mw.Logger)
	}

	// Third-party middleware
	s := stats.New()
	e.Use(s.Handler)
	// Stats route
	e.Get("/stats", func(c *echo.Context) {
		c.JSON(200, s.Data())
	})

	// Tracker routes
	e.Get("/:passkey/announce", handleAnnounce)
	e.Get("/:passkey/scrape", handleScrape)

	// Start server
	e.Run(*listen_host)
}
