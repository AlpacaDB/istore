package istore

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/golang/glog"
	"github.com/gregjones/httpcache"
	"github.com/syndtr/goleveldb/leveldb"
	levelutil "github.com/syndtr/goleveldb/leveldb/util"
)

const _DbFile = "/tmp/metadb"

const _PathIdSeq = "sys.seq"
const _PathSeqNS = "sys.ns.seq"

type ItemId uint64

func (id ItemId) Bytes() []byte {
	b := make([]byte, 8, 8)
	binary.PutUvarint(b, uint64(id))
	return b
}

func ToItemId(val []byte) ItemId {
	id, _ := binary.Uvarint(val)
	return ItemId(id)
}

func (id ItemId) Key() []byte {
	return append([]byte(_PathSeqNS), id.Bytes()...)
}

type ItemMeta struct{
	ItemId ItemId `json:"_id,omitempty"`
	MetaData map[string]interface{} `json:"metadata,omitempty"`
}

type Server struct {
	Client  *http.Client
	Cache   httpcache.Cache
	Db      *leveldb.DB
	idseq     ItemId
	idseqLock sync.RWMutex
}

func copyHeader(w http.ResponseWriter, r *http.Response, header string) {
	key := http.CanonicalHeaderKey(header)
	if value, ok := r.Header[key]; ok {
		w.Header()[key] = value
	}
}

func extractTargetURL(path string) string {
	r := regexp.MustCompile("^.+/([0-9a-z]+\\://.+)$")
	strs := r.FindStringSubmatch(path)

	if len(strs) > 1 {
		return strs[1]
	}
	return ""
}

func NewServer() *Server {
	cache := httpcache.NewMemoryCache()
	client := &http.Client{}
	client.Transport = httpcache.NewTransport(cache)
	db, err := leveldb.OpenFile(_DbFile, nil)
	if err != nil {
		glog.Error(err)
	}

	// the latest id sequence
	idseq, err := db.Get([]byte(_PathIdSeq), nil)
	if err == leveldb.ErrNotFound {
		idseq = ItemId(1).Bytes()
	}

	return &Server{
		Client: client,
		Cache:  cache,
		Db:     db,
		idseq:  ToItemId(idseq),
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	glog.Infof("%s %s %s", r.Method, r.URL, r.Proto)
	switch r.Method {
	case "POST", "PUT":
		s.ServePost(w, r)
	case "GET", "HEAD":
		s.ServeGet(w, r)
	default:
		msg := fmt.Sprintf("Not implemented method %s", r.Method)
		glog.Error(msg)
		http.Error(w, msg, http.StatusNotImplemented)
	}
}

func (s *Server) NextItemId() ItemId {
	// TODO: it could be achieved by sync/atomic instead of lock
	s.idseqLock.Lock()
	defer s.idseqLock.Unlock()

	for {
		val := s.idseq
		s.idseq++
		if s.idseq == 0 {
			panic("_id wrap around")
		}
		if has, err := s.Db.Has(val.Key(), nil); err != nil {
			panic(err)
		} else if !has{
			return val
		}
	}
}

func (s *Server) ServePost(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Path

	// read user input metadata
	value := r.FormValue("metadata")
	usermeta := map[string]interface{}{}
	if value != "" {
		if err := json.Unmarshal([]byte(value), &usermeta); err != nil {
			glog.Error(err)
			http.Error(w, "Error", http.StatusBadRequest)
			return
		}
	}

	meta := ItemMeta{}
	// fetch item from db if exists
	if data, err := s.Db.Get([]byte(key), nil); err == nil {
		if err := json.Unmarshal(data, &meta); err != nil {
			glog.Error("failed to parse json from db", err)
			// continue anyway as new item
		}
	}

	isnew := meta.ItemId == 0
	if isnew {
		meta.ItemId = s.NextItemId()
	}

	meta.MetaData = usermeta

	metastr, err := json.Marshal(&meta)
	if err != nil {
		glog.Error(err)
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}

	batch := new(leveldb.Batch)
	// User path -> metadata
	batch.Put([]byte(key), []byte(metastr))

	if isnew {
		itemId := meta.ItemId
		// ItemId -> User path
		batch.Put(itemId.Key(), []byte(key))
		// Update the sequence number.  This could be in race condition if
		// concurrent writer updates this at the same time, and it can go
		// backward in case of restart (as far as the system is up,
		// server.idseq never goes back).  The truth is kept in the
		// ItemId -> User path and the rule of id assignment is to look at this
		// ItemId key exclusively (see NextItemId()), so the uniqueness is
		// guaranteed by this ItemId key.  That means this sequence persistency
		// is nothing but a hint to quickly catch up to the latest value.
		batch.Put([]byte(_PathIdSeq), itemId.Bytes())
	}

	if err := s.Db.Write(batch, nil); err != nil {
		msg := fmt.Sprintf("put failed for %s: %v", key, err)
		glog.Error(msg)
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	w.Write(metastr)
}

func (s *Server) ServeList(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	iter := s.Db.NewIterator(levelutil.BytesPrefix([]byte(path)), nil)
	results := []interface{}{}
	for iter.Next() {
		result := map[string]interface{}{}
		result["filepath"] = string(iter.Key())

		var metadata interface{}
		value := iter.Value()
		if value != nil {
			reader := bytes.NewReader(value)
			decoder := json.NewDecoder(reader)
			decoder.Decode(&metadata)
		}
		result["metadata"] = metadata
		results = append(results, result)
	}
	iter.Release()
	err := iter.Error()
	if err != nil {
		msg := fmt.Sprint(err)
		glog.Error(msg)
		http.Error(w, "Error", http.StatusInternalServerError)
	}

	w.Header()["Content-type"] = []string{"application/json"}
	encoder := json.NewEncoder(w)
	if err := encoder.Encode(results); err != nil {
		glog.Error(err)
	}
}

func (s *Server) ServeGet(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	if strings.HasSuffix(path, "/") {
		s.ServeList(w, r)
		return
	}

	if _, err := s.Db.Get([]byte(path), nil); err != nil {
		if err == leveldb.ErrNotFound {
			http.NotFound(w, r)
			return
		}
		msg := fmt.Sprintf("error while reading %s: %v", path, err)
		glog.Error(msg)
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}

	urlstr := extractTargetURL(path)
	if urlstr == "" {
		msg := fmt.Sprintf("target not found in path %s", path)
		glog.Info(msg)
		http.Error(w, msg, http.StatusNotFound)
		return
	}

	client := s.Client
	resp, err := client.Get(urlstr)
	if err != nil {
		var msg string
		statusCode := http.StatusBadRequest
		if resp == nil {
			msg = fmt.Sprintf("%v", err)
		} else {
			msg = fmt.Sprintf("remote URL %q returned status: %v", urlstr, resp.Status)
			statusCode = resp.StatusCode
		}
		glog.Error(msg)
		http.Error(w, msg, statusCode)
		return
	}

	copyHeader(w, resp, "Last-Modified")
	copyHeader(w, resp, "Expires")
	copyHeader(w, resp, "Etag")
	copyHeader(w, resp, "Content-Length")
	copyHeader(w, resp, "Content-Type")
	io.Copy(w, resp.Body)
}

// -----
// some thoughts
// curl -X POST http://localhost:9999/mybucket/events/19/_search -d '
// {
//   "similar": {
//     "to": "self:///mybucket/events/19/foobar.jpg",
//     "by": "feature"
//   }
// }
// 
// curl -X POST http://localhost:9999/mybucket/events/19/_create_index -d '
// {
//   "by": "feature"
// }

