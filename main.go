package main

import (
	crand "crypto/rand"
	b64 "encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"runtime"
	"time"

	"golang.org/x/crypto/nacl/secretbox"
	"golang.org/x/net/websocket"
)

var s minediveServer

func bToMb(b uint64) uint64 {
	return b / 1024 / 1024
}

func printMemUsage() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	// For info on each, see: https://golang.org/pkg/runtime/#MemStats
	fmt.Printf("Alloc = %v MiB", bToMb(m.Alloc))
	fmt.Printf("\tTotalAlloc = %v MiB", bToMb(m.TotalAlloc))
	fmt.Printf("\tSys = %v MiB", bToMb(m.Sys))
	fmt.Printf("\tNumGC = %v\n", m.NumGC)
}

func incNonce(a []byte, dyn int) error {
	l := len(a)
	if l < dyn {
		dyn = l
	}
	for i := 1; i <= dyn; i++ {
		if a[l-i] < 0xff {
			a[l-i]++
			return nil
		}
		a[l-i] = 0
	}
	return errors.New("incNonce: nonce expired")
}

func getAlias(username string, gw *minediveClient) (string, error) {
	var alias string
	var err error
	enc := secretbox.Seal(gw.Nonce[:], []byte(username), &gw.Nonce, &gw.SecretKey)
	alias = b64.StdEncoding.EncodeToString(enc)
	return alias, err
}

func minediveDispatch(cli *minediveClient, jmsg []byte) {
	var err error
	var imsg idMsg
	err = json.Unmarshal(jmsg, &imsg)
	if err != nil {
		log.Println(err.Error())
	} else {
		log.Println("New msg:", imsg.Type)
		switch imsg.Type {
		case "username":
			var umsg usernameMsg
			err = json.Unmarshal(jmsg, &umsg)
			if err != nil {
				log.Panic(err.Error())
			}
			cli.Name = umsg.Name
			b64pk, _ := b64.StdEncoding.DecodeString(umsg.PK)
			copy(cli.PublicKey[:], b64pk[:32])
		case "ping":
			var datamsg dataMsg
			err = json.Unmarshal(jmsg, &datamsg)
			datamsg.Type = "pong"
			websocket.JSON.Send(cli.ws, datamsg)
		case "message":
			log.Println("message not used")
		case "getkey":
			var kreq keyReq
			err = json.Unmarshal(jmsg, &kreq)
			if err != nil {
				log.Panic(err.Error())
			}
			s.sendKey(cli, &kreq)
		case "getalias":
			log.Println("getalias not used")
		case "getpeers":
			s.sendPeer(cli)
		case "offer":
			var rtcmsg webrtcMsg
			err = json.Unmarshal(jmsg, &rtcmsg)
			if err != nil {
				log.Println(err.Error())
			} else {
				s.fwdToTarget(&rtcmsg)
			}
		case "answer":
			var rtcmsg webrtcMsg
			err = json.Unmarshal(jmsg, &rtcmsg)
			if err != nil {
				log.Println(err.Error())
			} else {
				s.fwdToTarget(&rtcmsg)
			}
		default:
			log.Println(imsg.Type)
		}
	}
}

func minediveAccept(ws *websocket.Conn) {
	log.Println(ws.Config())
	var cli minediveClient
	s.idMutex.Lock()
	cli.ID = s.nextID
	s.nextID++
	s.idMutex.Unlock()
	cli.ws = ws
	cli.RemoteAddr = ws.RemoteAddr().String()
	if _, err := io.ReadFull(crand.Reader, cli.SecretKey[:]); err != nil {
		panic(err)
	}
	var msg = idMsg{Type: "id", ID: cli.ID}
	websocket.JSON.Send(ws, msg)
	//log.Println("new ws client created with id: ", cli.ID, "from", cli.RemoteAddr)
	s.clientsMutex.Lock()
	s.clients = append(s.clients, &cli)
	s.clientsMutex.Unlock()
	for {
		var jmsg []byte
		err := websocket.Message.Receive(ws, &jmsg)
		if err != nil {
			log.Println(err)
			s.deleteClientByName(cli.Name)
			cli.ws.Close()
			s.dumpClients()
			log.Println(cli.Name, "disconnected")
			return
		}
		if jmsg != nil {
			minediveDispatch(&cli, jmsg)
		} else {
			log.Println("TIMEOUT")
			time.Sleep(300 * time.Millisecond)
		}

	}
}

func checkOrigin(config *websocket.Config, req *http.Request) (err error) {
	config.Origin, err = websocket.Origin(config, req)
	if err == nil {
		//log.Println(config.Origin)
	} else {
		//log.Println("CHECKORIGINERROR", err.Error())
		return err
	}
	return
}

func main() {
	certDir := flag.String("d", "", "Certificate and Key directory")
	plainHTTP := flag.Bool("plain-http", false, "Explic fallback on plain HTTP")
	port := flag.Int("port", 6501, "Listen port")
	flag.Parse()

	s.initMinediveServer()
	portString := fmt.Sprintf(":%d", *port)
	hs := &http.Server{
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		ReadHeaderTimeout: 20 * time.Second,
		Addr:              portString,
		Handler:           websocket.Handler(minediveAccept),
	}
	var err error
	if *plainHTTP == true {
		err = hs.ListenAndServe()
	} else {
		err = hs.ListenAndServeTLS(*certDir+"cert.pem", *certDir+"privkey.pem")
	}
	if err != nil {
		panic("ListenAndServe: " + err.Error())
	}
}
