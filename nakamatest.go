package nakamatest

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/ascii8/nakama-go"
	"github.com/assemblaj/gole"
	"github.com/google/uuid"
	"github.com/pion/stun"
	"github.com/spf13/viper"
)

type Session struct {
	remoteIp      string
	remotePort    string
	localIp       string
	localPort     string
	localPublicIp string
	mutex         sync.Mutex
}

func newSession() *Session {
	return &Session{
		mutex: sync.Mutex{},
	}
}

func (s *Session) PunchPort(port string, protocol string, close bool) (conn net.Conn, err error) {
	if protocol == "tcp" {
		return s.PunchTCPPort(port, close)
	} else if protocol == "udp" {
		return s.PunchUDPPort(port, close)
	}
	return conn, fmt.Errorf("unknown protocol")
}

func (s *Session) PunchTCPPort(port string, close bool) (net.Conn, error) {
	config := gole.ParseConfig([]string{"gole", "tcp",
		s.localIp + ":" + port,
		s.remoteIp + ":" + port})
	conn, err := gole.Punch(config)
	if err != nil && close {
		conn.Close()
	}
	return conn, err
}

func (s *Session) PunchUDPPort(port string, close bool) (net.Conn, error) {
	config := gole.ParseConfig([]string{"gole", "udp",
		s.localIp + ":" + port,
		s.remoteIp + ":" + port})
	fmt.Println(config)
	conn, err := gole.Punch(config)
	if err != nil && close {
		conn.Close()
	}
	return conn, err
}

func (s *Session) LocalIP() string {
	return s.localIp
}

func (s *Session) LocalPublicIP() string {
	return s.localPublicIp
}

func (s *Session) RemoteIP() string {
	return s.remoteIp
}

func getPublicIPFromSTUN() net.IP {
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

func getLocalIPAddress() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "", err
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)

	return localAddr.IP.String(), nil
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

func joinNakamaRoom(target string, myPort int, config Config) (*nakama.Conn, context.Context, *nakama.ChannelMsg, string, error) {
	deviceId := uuid.New().String()
	ctx, _ := context.WithCancel(context.Background())

	opts := []nakama.Option{
		nakama.WithURL(config.Urlstr),
		nakama.WithServerKey(config.ServerKey),
		nakama.WithExpiryGrace(480 * time.Second),
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

func InitiateP2PSession(serverConfig string, port int) (*Session, error) {
	config, err := readConfig(serverConfig)
	if err != nil {
		log.Fatal(err)
	}

	conn, ctx, ch1, id, err := joinNakamaRoom(config.Room, port, config)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	session := newSession()
	session.localPort = strconv.Itoa(port)

	return exchangeP2PInformation(id, conn, ctx, ch1, port, session)
}

func exchangeP2PInformation(id string, conn *nakama.Conn, ctx context.Context, ch1 *nakama.ChannelMsg, myPort int, session *Session) (*Session, error) {
	//ipsentto := make(map[string]bool)
	ipCh := make(chan string, 1)
	sentCh := make(chan bool)
	portCh := make(chan string, 1)
	doneCh := make(chan bool)
	timeout := time.After(480 * time.Second)
	ackReceived := false
	ipReceived := false
	portReceived := false

	conn.ChannelMessageHandler = func(ctx context.Context, msg *nakama.ChannelMessage) {
		if msg.Username != id {
			fmt.Printf("Received message: %s\n", msg.Content) // Add this line
			var m map[string]interface{}
			if err := json.Unmarshal([]byte(msg.Content), &m); err != nil {
				log.Fatal(err)
			}

			if m["msg"] == "hello" {
				addr := getPublicIPFromSTUN()
				if addr.To4() == nil {
					session.localPublicIp = "[" + addr.String() + "]"
				} else {
					session.localPublicIp = addr.String()
				}
				// Send my IP
				ipMsg := map[string]interface{}{
					"msg":  session.localPublicIp + ":" + strconv.Itoa(myPort),
					"code": float64(15),
				}
				log.Printf("Sending IP: %+v\n", ipMsg) // Add this line
				if _, err := conn.ChannelMessageSend(ctx, ch1.Id, ipMsg); err != nil {
					if err.Error() != "context canceled" {
						log.Fatalf("expected no error, got: %v", err)
					}
				}
				sentCh <- true
			} else if m["msg"] == "ack" {
				// Received acknowledgment from the other peer
				ackReceived = true
				if ipReceived && portReceived {
					doneCh <- true
				}
			} else {
				// go func() {
				ip, port, err := net.SplitHostPort(m["msg"].(string))
				if err != nil {
					log.Fatalf("expected no error, got: %v", err)
				}
				recvAddr := net.ParseIP(ip)
				if recvAddr != nil && recvAddr.To4() == nil {
					ip = "[" + ip + "]"
				}
				ipCh <- ip
				portCh <- port
				ipReceived = true
				portReceived = true
				portInt, err := strconv.Atoi(port)
				if err != nil {
					// ... handle error
					panic(err)
				}

				// ports := []int{portInt, 7550, 7600}
				fmt.Println(portInt)
				<-sentCh
				localAddr, _ := getLocalIPAddress()

				session.remotePort = port
				session.localIp = localAddr
				session.remoteIp = ip

				// Send back acknowledgment
				ackMsg := map[string]interface{}{
					"msg":  "ack",
					"code": float64(15),
				}
				if _, err := conn.ChannelMessageSend(ctx, ch1.Id, ackMsg); err != nil {
					if err.Error() != "context canceled" {
						log.Fatalf("expected no error, got: %v", err)
					}
				}
				if ackReceived {
					doneCh <- true // Signal that IP and port have been obtained
				}
			}
		}
	}

	go func() {
		ticker := time.NewTicker(time.Second * 1)
		defer ticker.Stop()
		for range ticker.C {
			if _, err := conn.ChannelMessageSend(ctx, ch1.Id, helloMsg); err != nil {
				if err.Error() != "context canceled" {
					log.Fatalf("expected no error, got: %v", err)
				}
			}
			fmt.Printf("Sent hello message: %+v\n", helloMsg) // Add this line
		}
	}()

	select {
	case <-doneCh: // Wait here until IP and port are obtained
		fmt.Println("Returning IP and Port")
		fmt.Println(session.remoteIp)
		fmt.Println(session.remotePort)
		return session, nil
	case <-timeout: // Timeout occurred before IP and port were obtained
		return nil, fmt.Errorf("timeout waiting for IP and port")
	}
}
