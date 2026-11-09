// Command fakecloudflare stands in for Cloudflare's DNS API and the zone's
// nameserver in TestWildcardCertificateThroughDNSChallenge and
// TestNodeDeploysAndServes. It answers the calls Caddy's Cloudflare module
// makes for one zone, and, given -account, the ones Loomplane's Cloudflare
// connection and DNS adapter make. It accepts only the expected token,
// serves the zone's TXT, A and AAAA records over DNS, and says on
// :8080/status how many calls carried the token. It never prints a token.
package main

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"

	"golang.org/x/net/dns/dnsmessage"
)

type record struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	ZoneID  string `json:"zone_id"`
	Proxied bool   `json:"proxied"`
	Comment string `json:"comment"`
}

type fake struct {
	token, zoneID, zone, account string
	ip                           net.IP

	mu                   sync.Mutex
	records              map[string]record
	authorized, rejected int
	created              int
}

func main() {
	f := &fake{records: map[string]record{}}
	var ip, cert, key, httpAPI string
	flag.StringVar(&f.token, "token", "", "the only token accepted")
	flag.StringVar(&f.zoneID, "zone-id", "", "Cloudflare ID of the zone")
	flag.StringVar(&f.zone, "zone", "", "zone name")
	flag.StringVar(&ip, "ip", "", "this server's address, the zone's nameserver")
	flag.StringVar(&cert, "cert", "", "TLS certificate for api.cloudflare.com")
	flag.StringVar(&key, "key", "", "TLS key")
	flag.StringVar(&f.account, "account", "", "the Cloudflare account that owns the zone; set, the fake also answers Loomplane's own calls")
	flag.StringVar(&httpAPI, "http-api", "", "also serve the API over plain HTTP on this address, such as :8081")
	flag.Parse()
	f.ip = net.ParseIP(ip).To4()
	if f.token == "" || f.zoneID == "" || f.zone == "" || f.ip == nil {
		log.Fatal("token, zone-id, zone and ip are required")
	}
	go f.serveDNS()
	if httpAPI != "" {
		go func() { log.Fatal(http.ListenAndServe(httpAPI, http.HandlerFunc(f.api))) }()
	}
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/status", f.status)
		mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("wildcard-backend\n")) })
		log.Fatal(http.ListenAndServe(":8080", mux))
	}()
	log.Fatal(http.ListenAndServeTLS(":443", cert, key, http.HandlerFunc(f.api)))
}

func (f *fake) status(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]int{"authorized": f.authorized, "rejected": f.rejected, "created": f.created})
}

func reply(w http.ResponseWriter, status int, result any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if status >= 300 {
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":10000,"message":"refused"}],"result":null}`))
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "messages": []any{}, "result": result, "result_info": map[string]int{"page": 1, "total_pages": 1}})
}

func (f *fake) absolute(name string) string {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	if name == "@" || name == "" {
		return f.zone
	}
	if name != f.zone && !strings.HasSuffix(name, "."+f.zone) {
		name += "." + f.zone
	}
	return name
}

func (f *fake) api(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		f.rejected++
		reply(w, http.StatusForbidden, nil)
		return
	}
	f.authorized++
	path := strings.TrimPrefix(r.URL.Path, "/client/v4")
	records := "/zones/" + f.zoneID + "/dns_records"
	account := "/accounts/" + f.account
	zone := map[string]any{"id": f.zoneID, "name": f.zone, "status": "active", "name_servers": []string{"ns." + f.zone}, "account": map[string]string{"id": f.account}}
	switch {
	case f.account != "" && r.Method == http.MethodGet && (path == account+"/tokens/verify" || path == "/user/tokens/verify"):
		reply(w, http.StatusOK, map[string]string{"id": "fake-token", "status": "active"})
	case f.account != "" && r.Method == http.MethodGet && path == account:
		reply(w, http.StatusOK, map[string]string{"id": f.account, "name": "Fake Cloudflare"})
	case f.account != "" && strings.HasPrefix(path, account+"/registrar"):
		// The token holds no registrar access.
		reply(w, http.StatusForbidden, nil)
	case f.account != "" && r.Method == http.MethodGet && path == "/zones/"+f.zoneID:
		reply(w, http.StatusOK, zone)
	case r.Method == http.MethodGet && path == "/zones":
		zones := []map[string]any{}
		q := r.URL.Query()
		if name := strings.TrimSuffix(q.Get("name"), "."); (name == "" || name == f.zone) && (q.Get("account.id") == "" || q.Get("account.id") == f.account) {
			zones = append(zones, zone)
		}
		reply(w, http.StatusOK, zones)
	case r.Method == http.MethodGet && path == records:
		q := r.URL.Query()
		out := []record{}
		for _, rec := range f.records {
			if (q.Get("type") == "" || q.Get("type") == rec.Type) && (q.Get("name") == "" || f.absolute(q.Get("name")) == rec.Name) {
				out = append(out, rec)
			}
		}
		reply(w, http.StatusOK, out)
	case r.Method == http.MethodPost && path == records:
		var rec record
		if err := json.NewDecoder(r.Body).Decode(&rec); err != nil || !validContent(rec) {
			reply(w, http.StatusBadRequest, nil)
			return
		}
		id := make([]byte, 8)
		_, _ = rand.Read(id)
		rec.ID, rec.Name, rec.ZoneID = hex.EncodeToString(id), f.absolute(rec.Name), f.zoneID
		f.records[rec.ID] = rec
		f.created++
		reply(w, http.StatusOK, rec)
	case r.Method == http.MethodPatch && strings.HasPrefix(path, records+"/"):
		id := strings.TrimPrefix(path, records+"/")
		rec, ok := f.records[id]
		var in record
		if !ok || json.NewDecoder(r.Body).Decode(&in) != nil || !validContent(in) {
			reply(w, http.StatusBadRequest, nil)
			return
		}
		rec.Type, rec.Name, rec.Content, rec.TTL, rec.Proxied, rec.Comment = in.Type, f.absolute(in.Name), in.Content, in.TTL, in.Proxied, in.Comment
		f.records[id] = rec
		reply(w, http.StatusOK, rec)
	case r.Method == http.MethodDelete && strings.HasPrefix(path, records+"/"):
		id := strings.TrimPrefix(path, records+"/")
		rec, ok := f.records[id]
		if !ok {
			reply(w, http.StatusNotFound, nil)
			return
		}
		delete(f.records, id)
		reply(w, http.StatusOK, rec)
	default:
		reply(w, http.StatusNotFound, nil)
	}
}

// validContent accepts TXT records, and A and AAAA records that hold an
// address of their family. Cloudflare refuses anything else, Loomplane's
// DNS Write probe among it.
func validContent(rec record) bool {
	ip := net.ParseIP(rec.Content)
	switch rec.Type {
	case "TXT":
		return true
	case "A":
		return ip != nil && ip.To4() != nil
	case "AAAA":
		return ip != nil && ip.To4() == nil
	}
	return false
}

func (f *fake) serveDNS() {
	conn, err := net.ListenPacket("udp", ":53")
	if err != nil {
		log.Fatal(err)
	}
	stream, err := net.Listen("tcp", ":53")
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		for {
			c, acceptErr := stream.Accept()
			if acceptErr != nil {
				continue
			}
			go f.serveDNSStream(c)
		}
	}()
	buf := make([]byte, 4096)
	for {
		n, addr, readErr := conn.ReadFrom(buf)
		if readErr != nil {
			continue
		}
		if out, answerErr := f.answer(buf[:n]); answerErr == nil {
			_, _ = conn.WriteTo(out, addr)
		}
	}
}

// serveDNSStream answers length-prefixed queries over TCP, as Pebble asks.
func (f *fake) serveDNSStream(c net.Conn) {
	defer c.Close()
	for {
		var size [2]byte
		if _, err := io.ReadFull(c, size[:]); err != nil {
			return
		}
		query := make([]byte, binary.BigEndian.Uint16(size[:]))
		if _, err := io.ReadFull(c, query); err != nil {
			return
		}
		out, err := f.answer(query)
		if err != nil {
			return
		}
		binary.BigEndian.PutUint16(size[:], uint16(len(out)))
		if _, err = c.Write(append(size[:], out...)); err != nil {
			return
		}
	}
}

// answer serves the zone: its SOA and NS, the nameserver's address, and
// the TXT, A and AAAA records the API holds. Every other name is empty.
func (f *fake) answer(query []byte) ([]byte, error) {
	var p dnsmessage.Parser
	header, err := p.Start(query)
	if err != nil {
		return nil, err
	}
	q, err := p.Question()
	if err != nil {
		return nil, err
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: header.ID, Response: true, Authoritative: true, RecursionDesired: header.RecursionDesired})
	b.EnableCompression()
	if err = b.StartQuestions(); err != nil {
		return nil, err
	}
	if err = b.Question(q); err != nil {
		return nil, err
	}
	if err = b.StartAnswers(); err != nil {
		return nil, err
	}
	name := strings.ToLower(strings.TrimSuffix(q.Name.String(), "."))
	ns := dnsmessage.MustNewName("ns." + f.zone + ".")
	rh := dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, TTL: 1}
	switch {
	case q.Type == dnsmessage.TypeSOA && name == f.zone:
		err = b.SOAResource(rh, dnsmessage.SOAResource{NS: ns, MBox: dnsmessage.MustNewName("hostmaster." + f.zone + "."), Serial: 1, Refresh: 1, Retry: 1, Expire: 1, MinTTL: 1})
	case q.Type == dnsmessage.TypeNS && name == f.zone:
		err = b.NSResource(rh, dnsmessage.NSResource{NS: ns})
	case q.Type == dnsmessage.TypeA && name == "ns."+f.zone:
		err = b.AResource(rh, dnsmessage.AResource{A: [4]byte(f.ip)})
	case q.Type == dnsmessage.TypeTXT || q.Type == dnsmessage.TypeA || q.Type == dnsmessage.TypeAAAA:
		f.mu.Lock()
		for _, rec := range f.records {
			if rec.Name != name || err != nil {
				continue
			}
			switch {
			case q.Type == dnsmessage.TypeTXT && rec.Type == "TXT":
				err = b.TXTResource(rh, dnsmessage.TXTResource{TXT: []string{strings.Trim(rec.Content, `"`)}})
			case q.Type == dnsmessage.TypeA && rec.Type == "A":
				err = b.AResource(rh, dnsmessage.AResource{A: [4]byte(net.ParseIP(rec.Content).To4())})
			case q.Type == dnsmessage.TypeAAAA && rec.Type == "AAAA":
				err = b.AAAAResource(rh, dnsmessage.AAAAResource{AAAA: [16]byte(net.ParseIP(rec.Content).To16())})
			}
		}
		f.mu.Unlock()
	}
	if err != nil {
		return nil, err
	}
	return b.Finish()
}
