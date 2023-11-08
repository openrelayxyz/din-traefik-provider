package main

import (
	"os"
	"bytes"
	"time"
	"context"
	"log"
	"github.com/gorilla/websocket"
	"net/http"
	"io/ioutil"
	"encoding/json"
	"strconv"
	"fmt"
)

func MonitorBlock(ctx context.Context, url string, ch chan<- int64) {
	var conn *websocket.Conn
	var err error
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	go func () {
		for {
			if ctx.Err() != nil {
				return
			}
			conn, _, err = websocket.DefaultDialer.Dial(url, nil)
			if err != nil {
				log.Printf("Failed to dial websockets for %v: %v", url, err.Error())
				time.Sleep(100 * time.Millisecond)
				continue
			}
			conn.WriteMessage(websocket.TextMessage, []byte(`{"id":0, "jsonrpc":"2.0", "method":"eth_subscribe", "params": ["newHeads"]}`))
			_, msg, err := conn.ReadMessage() 
			log.Printf("Message (%v): %v", err, string(msg))
			for {
				_, msg, err = conn.ReadMessage() 
				if err != nil {
					log.Printf("Failed to read message on websockets for %v: %v", url, err.Error())
					break
				}
				log.Printf("Msg (%v): %v", err, string(msg))
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
}


type provider struct {
	WSURL string `json:"wsurl"`
}

type newHeadsMessage struct {
	Params struct {
		Result struct {
			Number string `json:"number"`
		} `json:"result"`
	} `json:"params"`
}

func main() {
	url := os.Args[1]
	resp, err := http.Get(url)
	if err != nil {
		panic(err.Error())
	}
	var providers []*provider
	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		panic(err.Error())
	}
	if err := json.Unmarshal(data, &providers); err != nil {
		panic(err.Error())
	}
	errCh := make(chan error)
	for i, p := range providers {
		go func(i int, p *provider ) {
				for {
					conn, _, err := websocket.DefaultDialer.Dial(p.WSURL, nil)
					if err != nil {
						log.Printf("Failed to dial websockets for %v: %v", url, err.Error())
						time.Sleep(100 * time.Millisecond)
						continue
					}
					conn.WriteMessage(websocket.TextMessage, []byte(`{"id":0, "jsonrpc":"2.0", "method":"eth_subscribe", "params": ["newHeads"]}`))
					_, msg, err := conn.ReadMessage() 
					log.Printf("Message (%v): %v", err, string(msg))
					for {
						_, msg, err = conn.ReadMessage() 
						if err != nil {
							log.Printf("Failed to read message on websockets for %v: %v", url, err.Error())
							break
						}
						log.Printf("Msg (%v): %v", err, string(msg))
						var nhm newHeadsMessage
						json.Unmarshal(msg, &nhm)
						num, err := strconv.ParseInt(nhm.Params.Result.Number, 0, 64)
						if err != nil {
							log.Printf("Error parsing number from head: %v - %v", nhm.Params.Result.Number, err.Error())
						} else {
							http.Post(url, "application/json", bytes.NewBuffer([]byte(fmt.Sprintf(`{"%v": %v}`, i, num))))
						}
					}
					time.Sleep(100 * time.Millisecond)
				}
		}(i, p)
	}
	panic(<-errCh)
}