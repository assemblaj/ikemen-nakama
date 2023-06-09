package nakamatest

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/ascii8/nakama-go"
	"github.com/google/uuid"
	"github.com/pion/stun"
	"github.com/spf13/viper"
)

func getIP() net.IP {
	conn, err := net.Dial("udp", "stun.l.google.com:19302")
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	transactionID := stun.NewTransactionID()

	msg := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	msg.TransactionID = transactionID

	if _, err = conn.Write(msg.Raw); err != nil {
		panic(err)
	}

	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		panic(err)
	}

	msg = new(stun.Message)
	msg.Raw = buf[:n]
	if err = msg.Decode(); err != nil {
		panic(err)
	}

	mappedAddress := &stun.XORMappedAddress{}
	if err = mappedAddress.GetFrom(msg); err != nil {
		panic(err)
	}

	return mappedAddress.IP
}

func getLocalIP() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "", err
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)

	return localAddr.IP.String(), nil
}

func getFreePort() (port int, err error) {
	var a *net.TCPAddr
	if a, err = net.ResolveTCPAddr("tcp", "localhost:0"); err == nil {
		var l *net.TCPListener
		if l, err = net.ListenTCP("tcp", a); err == nil {
			defer l.Close()
			return l.Addr().(*net.TCPAddr).Port, nil
		}
	}
	return
}

type Config struct {
	Urlstr    string `mapstructure:"urlstr"`
	ServerKey string `mapstructure:"server_key"`
	Room      string `mapstructure:"room"`
}

func readConfig(filePath string) (Config, error) {
	viper.SetConfigFile(filePath)

	if err := viper.ReadInConfig(); err != nil {
		return Config{}, fmt.Errorf("failed to read the configuration file: %s", err)
	}

	var config Config
	if err := viper.Unmarshal(&config); err != nil {
		return Config{}, fmt.Errorf("failed to unmarshal the configuration: %s", err)
	}

	return config, nil
}

func joinRoom(target string, myPort int, config Config) (*nakama.Conn, context.Context, *nakama.ChannelMsg, string, error) {
	deviceId := uuid.New().String()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opts := []nakama.Option{
		nakama.WithURL(config.Urlstr),
		nakama.WithServerKey(config.ServerKey),
	}
	cl := nakama.New(opts...)

	if err := cl.AuthenticateDevice(ctx, deviceId, true, deviceId); err != nil {
		return nil, nil, nil, deviceId, err
	}

	conn, err := cl.NewConn(ctx, []nakama.ConnOption{nakama.WithConnFormat("json")}...)
	if err != nil {
		return nil, nil, nil, deviceId, err
	}

	ch1, err := conn.ChannelJoin(ctx, target, nakama.ChannelType_ROOM, true, false)
	if err != nil {
		return nil, nil, nil, deviceId, err
	}

	return conn, ctx, ch1, deviceId, nil
}

var helloMsg map[string]interface{} = map[string]interface{}{
	"msg":  "hello",
	"code": float64(15),
}

func GetPublicAddr(serverConfig string, port int) (string, string, error) {
	config, err := readConfig(serverConfig)
	if err != nil {
		log.Fatal(err)
	}

	conn, ctx, ch1, id, err := joinRoom(config.Room, port, config)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	return getAddr(id, conn, ctx, ch1, port)
}

func getAddr(id string, conn *nakama.Conn, ctx context.Context, ch1 *nakama.ChannelMsg, myPort int) (string, string, error) {
	ipsentto := make(map[string]bool)
	ip := ""
	port := ""
	doneCh := make(chan bool)
	timeout := time.After(30 * time.Second) // Timeout after 30 seconds, adjust as necessary

	conn.ChannelMessageHandler = func(ctx context.Context, msg *nakama.ChannelMessage) {
		if msg.Username != id {
			fmt.Printf("Received message: %s\n", msg.Content) // Add this line
			var m map[string]interface{}
			if err := json.Unmarshal([]byte(msg.Content), &m); err != nil {
				log.Fatal(err)
			}

			if m["msg"] == "hello" {
				if _, sent := ipsentto[msg.Username]; !sent {
					addr := getIP().String()
					// Send my IP
					ipMsg := map[string]interface{}{
						"msg":  addr + ":" + strconv.Itoa(myPort),
						"code": float64(15),
					}
					log.Printf("Sending IP: %+v\n", ipMsg) // Add this line
					if _, err := conn.ChannelMessageSend(ctx, ch1.Id, ipMsg); err != nil {
						log.Fatalf("expected no error, got: %v", err)
					}
					ipsentto[msg.Username] = true
				}
			} else {
				// go func() {
				ipAndPort := strings.Split(m["msg"].(string), ":")
				ip = ipAndPort[0]
				port = ipAndPort[1]
				fmt.Println(ip)
				fmt.Println(port)
				doneCh <- true // Signal that IP and port have been obtained
			}
		}
	}

	go func() {
		ticker := time.NewTicker(time.Second * 1)
		defer ticker.Stop()
		for range ticker.C {
			if ip == "" { // assume there is one other peer
				if _, err := conn.ChannelMessageSend(ctx, ch1.Id, helloMsg); err != nil {
					log.Fatalf("expected no error, got: %v", err)
				}
				fmt.Printf("Sent hello message: %+v\n", helloMsg) // Add this line
			} else {
				break
			}
		}
	}()

	select {
	case <-doneCh: // Wait here until IP and port are obtained
		return ip, port, nil
	case <-timeout: // Timeout occurred before IP and port were obtained
		return "", "", fmt.Errorf("timeout waiting for IP and port")
	}
}
