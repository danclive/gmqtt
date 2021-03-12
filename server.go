package mqtt

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/danclive/nson-go"

	"go.uber.org/zap"

	retained_trie "github.com/danclive/mqtt/retained/trie"
	subscription_trie "github.com/danclive/mqtt/subscription/trie"

	"github.com/danclive/mqtt/packets"
	"github.com/danclive/mqtt/retained"
	"github.com/danclive/mqtt/subscription"
)

var (
	// ErrInvalWsMsgType [MQTT-6.0.0-1]
	ErrInvalWsMsgType = errors.New("invalid websocket message type")
	statusPanic       = "invalid server status"
)

// Default configration
const (
	DefaultMsgRouterLen  = 4096
	DefaultRegisterLen   = 2048
	DefaultUnRegisterLen = 2048
)

// Server status
const (
	serverStatusInit = iota
	serverStatusStarted
)

var zaplog *zap.Logger

func init() {
	zaplog = zap.NewNop()
}

// LoggerWithField add fields to a new logger.
// Plugins can use this method to add plugin name field.
func LoggerWithField(fields ...zap.Field) *zap.Logger {
	return zaplog.With(fields...)
}

// Server interface represents a mqtt server instance.
type Server interface {
	// SubscriptionStore returns the subscription.Store.
	SubscriptionStore() subscription.Store
	// RetainedStore returns the retained.Store.
	RetainedStore() retained.Store
	// PublishService returns the PublishService
	PublishService() PublishService
	// Client return the client specified by clientID.
	Client(clientID string) Client
	// GetConfig returns the config of the server
	GetConfig() Config
	// GetStatsManager returns StatsManager
	GetStatsManager() StatsManager
	Init(opts ...Options)
	Run()
	Stop(ctx context.Context) error
}

// server represents a mqtt server instance.
// Create a server by using NewServer()
type server struct {
	wg      sync.WaitGroup
	mu      sync.RWMutex //gard clients & offlineClients map
	status  int32        //server status
	clients map[string]*client
	// offlineClients store the disconnected time of all disconnected clients
	// with valid session(not expired). Key by clientID
	offlineClients  map[string]time.Time
	tcpListener     []net.Listener //tcp listeners
	websocketServer []*WsServer    //websocket serverStop
	exitChan        chan struct{}

	retainedDB      retained.Store
	subscriptionsDB subscription.Store //store subscriptions

	msgRouter  chan *msgRouter
	register   chan *register   //register session
	unregister chan *unregister //unregister session
	config     Config
	hooks      []Hooks
	plugins    []Plugin

	statsManager   StatsManager
	publishService PublishService
}

func (srv *server) SubscriptionStore() subscription.Store {
	return srv.subscriptionsDB
}

func (srv *server) RetainedStore() retained.Store {
	return srv.retainedDB
}

func (srv *server) PublishService() PublishService {
	return srv.publishService
}

func (srv *server) checkStatus() {
	if srv.Status() != serverStatusInit {
		panic(statusPanic)
	}
}

type DeliveryMode int

const (
	Overlap  DeliveryMode = 0
	OnlyOnce DeliveryMode = 1
)

type Config struct {
	RetryInterval              time.Duration
	RetryCheckInterval         time.Duration
	SessionExpiryInterval      time.Duration
	SessionExpiryCheckInterval time.Duration
	QueueQos0Messages          bool
	MaxInflight                int
	MaxAwaitRel                int
	MaxMsgQueue                int
	DeliveryMode               DeliveryMode
	MsgRouterLen               int
	RegisterLen                int
	UnregisterLen              int
}

// DefaultConfig default config used by NewServer()
var DefaultConfig = Config{
	RetryInterval:              20 * time.Second,
	RetryCheckInterval:         20 * time.Second,
	SessionExpiryInterval:      0 * time.Second,
	SessionExpiryCheckInterval: 0 * time.Second,
	QueueQos0Messages:          true,
	MaxInflight:                32,
	MaxAwaitRel:                100,
	MaxMsgQueue:                1000,
	DeliveryMode:               OnlyOnce,
	MsgRouterLen:               DefaultMsgRouterLen,
	RegisterLen:                DefaultRegisterLen,
	UnregisterLen:              DefaultUnRegisterLen,
}

// GetConfig returns the config of the server
func (srv *server) GetConfig() Config {
	return srv.config
}

// GetStatsManager returns StatsManager
func (srv *server) GetStatsManager() StatsManager {
	return srv.statsManager
}

//session register
type register struct {
	client  *client
	connect *packets.Connect
	error   error
}

// session unregister
type unregister struct {
	client *client
	done   chan struct{}
}

type msgRouter struct {
	msg      packets.Message
	clientID string
	// if set to false, must set clientID to specify the client to send
	match bool
}

// Status returns the server status
func (srv *server) Status() int32 {
	return atomic.LoadInt32(&srv.status)
}

func (srv *server) registerHandler(register *register) {
	client := register.client
	defer close(client.ready)

	connect := register.connect

	if connect.AckCode != packets.CodeAccepted {
		err := errors.New("reject connection, ack code:" + strconv.Itoa(int(connect.AckCode)))

		ack := connect.NewConnackPacket(false)
		client.writePacket(ack)

		register.error = err
		return
	}

	// ack code set in Connack Packet
	var code uint8

	for _, hooks := range srv.hooks {
		if hooks.OnConnect != nil {
			co := hooks.OnConnect(client)
			if co > code {
				code = co
			}
		}
	}

	connect.AckCode = code

	if code != packets.CodeAccepted {
		err := errors.New("reject connection, ack code:" + strconv.Itoa(int(code)))

		ack := connect.NewConnackPacket(false)
		client.writePacket(ack)
		client.setError(err)

		register.error = err
		return
	}

	// client 连接后触发
	for _, hooks := range srv.hooks {
		if hooks.OnConnected != nil {
			hooks.OnConnected(client)
		}
	}

	// 统计数据
	srv.statsManager.addClientConnected()
	srv.statsManager.addSessionActive()

	client.setConnectedAt(time.Now())

	var sessionReuse bool // 是否重用 session

	srv.mu.Lock()
	defer srv.mu.Unlock()

	var oldSession *session
	oldClient, oldExist := srv.clients[client.opts.clientID]
	srv.clients[client.opts.clientID] = client

	if oldExist {
		oldSession = oldClient.session

		if oldClient.IsConnected() {

			zaplog.Info("logging with duplicate ClientID",
				zap.String("remote", client.rwc.RemoteAddr().String()),
				zap.String("client_id", client.OptionsReader().ClientID()),
			)

			oldClient.setSwitching()

			<-oldClient.Close()

			if oldClient.opts.willFlag {
				willMsg := &packets.Publish{
					Dup:       false,
					Qos:       oldClient.opts.willQos,
					Retain:    oldClient.opts.willRetain,
					TopicName: []byte(oldClient.opts.willTopic),
					Payload:   oldClient.opts.willPayload,
				}

				go func() {
					msgRouter := &msgRouter{msg: messageFromPublish(willMsg), match: true}
					srv.msgRouter <- msgRouter
				}()
			}

			if !client.opts.cleanSession && !oldClient.opts.cleanSession { //reuse old session
				sessionReuse = true
			}
		} else if oldClient.IsDisConnected() {
			if !client.opts.cleanSession {
				sessionReuse = true
			} else {
				// session 终止时触发
				for _, hooks := range srv.hooks {
					if hooks.OnSessionTerminated != nil {
						hooks.OnSessionTerminated(oldClient, ConflictTermination)
					}
				}
			}
		} else {
			// session 终止时触发
			for _, hooks := range srv.hooks {
				if hooks.OnSessionTerminated != nil {
					hooks.OnSessionTerminated(oldClient, ConflictTermination)
				}
			}
		}
	}

	ack := connect.NewConnackPacket(sessionReuse)
	client.out <- ack

	client.setConnected()

	if sessionReuse {
		//发送还未确认的消息和离线消息队列 sending inflight messages & offline message
		client.session.unackpublish = oldSession.unackpublish
		client.statsManager = oldClient.statsManager
		//send unacknowledged publish
		oldSession.inflightMu.Lock()
		for e := oldSession.inflight.Front(); e != nil; e = e.Next() {
			if inflight, ok := e.Value.(*inflightElem); ok {
				pub := inflight.packet
				pub.Dup = true
				client.statsManager.decInflightCurrent(1)
				client.onlinePublish(pub)
			}
		}
		oldSession.inflightMu.Unlock()
		//send unacknowledged pubrel
		oldSession.awaitRelMu.Lock()
		for e := oldSession.awaitRel.Front(); e != nil; e = e.Next() {
			if await, ok := e.Value.(*awaitRelElem); ok {
				pid := await.pid
				pubrel := &packets.Pubrel{
					FixHeader: &packets.FixHeader{
						PacketType:   packets.PUBREL,
						Flags:        packets.FLAG_PUBREL,
						RemainLength: 2,
					},
					PacketID: pid,
				}
				client.setAwaitRel(pid)
				client.session.setPacketID(pid)
				client.statsManager.decAwaitCurrent(1)
				client.out <- pubrel
			}
		}
		oldSession.awaitRelMu.Unlock()

		//send offline msg
		oldSession.msgQueueMu.Lock()
		for e := oldSession.msgQueue.Front(); e != nil; e = e.Next() {
			if publish, ok := e.Value.(*packets.Publish); ok {
				client.statsManager.messageDequeue(1)
				client.onlinePublish(publish)
			}
		}
		oldSession.msgQueueMu.Unlock()

		zaplog.Info("logged in with session reuse",
			zap.String("remote_addr", client.rwc.RemoteAddr().String()),
			zap.String("client_id", client.opts.clientID))
	} else {
		if oldExist {
			srv.subscriptionsDB.UnsubscribeAll(client.opts.clientID)
		}
		zaplog.Info("logged in with new session",
			zap.String("remote_addr", client.rwc.RemoteAddr().String()),
			zap.String("client_id", client.opts.clientID),
		)
	}
	if sessionReuse {
		// session 恢复时触发
		for _, hooks := range srv.hooks {
			if hooks.OnSessionResumed != nil {
				hooks.OnSessionResumed(client)
			}
		}
	} else {
		// session 创建时触发
		for _, hooks := range srv.hooks {
			if hooks.OnSessionCreated != nil {
				hooks.OnSessionCreated(client)
			}
		}
	}

	delete(srv.offlineClients, client.opts.clientID)
}

func (srv *server) unregisterHandler(unregister *unregister) {
	defer close(unregister.done)

	client := unregister.client
	client.setDisConnected()

	select {
	case <-client.ready:
	default:
		// default means the client is closed before srv.registerHandler(),
		// session is not created, so there is no need to unregister.
		return
	}

clearIn:
	for {
		select {
		case p := <-client.in:
			if _, ok := p.(*packets.Disconnect); ok {
				client.cleanWillFlag = true
			}
		default:
			break clearIn
		}
	}

	if !client.cleanWillFlag && client.opts.willFlag {
		willMsg := &packets.Publish{
			Dup:       false,
			Qos:       client.opts.willQos,
			Retain:    false,
			TopicName: []byte(client.opts.willTopic),
			Payload:   client.opts.willPayload,
		}

		msg := messageFromPublish(willMsg)

		go func() {
			msgRouter := &msgRouter{msg: msg, match: true}
			client.server.msgRouter <- msgRouter
		}()
	}

	if client.opts.cleanSession {
		zaplog.Info("logged out and cleaning session",
			zap.String("remote_addr", client.rwc.RemoteAddr().String()),
			zap.String("client_id", client.OptionsReader().ClientID()),
		)

		srv.mu.Lock()
		srv.removeSession(client.opts.clientID)
		srv.mu.Unlock()

		// session 终止时触发
		for _, hooks := range srv.hooks {
			if hooks.OnSessionTerminated != nil {
				hooks.OnSessionTerminated(client, NormalTermination)
			}
		}

		// 统计数据
		srv.statsManager.messageDequeue(client.statsManager.GetStats().MessageStats.QueuedCurrent)
	} else {
		//store session 保持session
		srv.mu.Lock()
		srv.offlineClients[client.opts.clientID] = time.Now()
		srv.mu.Unlock()

		zaplog.Info("logged out and storing session",
			zap.String("remote_addr", client.rwc.RemoteAddr().String()),
			zap.String("client_id", client.OptionsReader().ClientID()),
		)
		//clear out
	clearOut:
		for {
			select {
			case p := <-client.out:
				if p, ok := p.(*packets.Publish); ok {
					client.msgEnQueue(p)
				}
			default:
				break clearOut
			}
		}

		// 统计数据
		srv.statsManager.addSessionInactive()
	}
}

// 所有进来的 msg都会分配pid，指定pid重传的不在这里处理
func (srv *server) msgRouterHandler(m *msgRouter) {
	msg := m.msg

	var matched subscription.ClientTopics

	if m.match {
		matched = srv.subscriptionsDB.GetTopicMatched(msg.Topic())

		if m.clientID != "" {
			tmp, ok := matched[m.clientID]

			matched = make(subscription.ClientTopics)

			if ok {
				matched[m.clientID] = tmp
			}
		}
	} else {
		// no need to search in subscriptionsDB.
		matched = make(subscription.ClientTopics)

		matched[m.clientID] = append(matched[m.clientID], packets.Topic{
			Qos:  msg.Qos(),
			Name: msg.Topic(),
		})
	}

	srv.mu.RLock()
	defer srv.mu.RUnlock()

	for cid, topics := range matched {
		if srv.config.DeliveryMode == Overlap {
			for _, t := range topics {
				if c, ok := srv.clients[cid]; ok {
					publish := messageToPublish(msg)

					if publish.Qos > t.Qos {
						publish.Qos = t.Qos
					}

					publish.Dup = false

					c.publish(publish)
				}
			}
		} else {
			// deliver once
			var maxQos uint8

			for _, t := range topics {
				if t.Qos > maxQos {
					maxQos = t.Qos
				}
				if maxQos == packets.QOS_2 {
					break
				}
			}

			if c, ok := srv.clients[cid]; ok {
				publish := messageToPublish(msg)

				if publish.Qos > maxQos {
					publish.Qos = maxQos
				}

				publish.Dup = false

				c.publish(publish)
			}
		}
	}
}
func (srv *server) removeSession(clientID string) {
	delete(srv.clients, clientID)
	delete(srv.offlineClients, clientID)
	srv.subscriptionsDB.UnsubscribeAll(clientID)
}

// sessionExpireCheck 判断是否超时
// sessionExpireCheck check and terminate expired sessions
func (srv *server) sessionExpireCheck() {
	expire := srv.config.SessionExpiryCheckInterval
	if expire == 0 {
		return
	}

	now := time.Now()

	srv.mu.Lock()
	for id, disconnectedAt := range srv.offlineClients {
		if now.Sub(disconnectedAt) >= expire {
			if client, _ := srv.clients[id]; client != nil {
				srv.removeSession(id)

				// session 终止时触发
				for _, hooks := range srv.hooks {
					if hooks.OnSessionTerminated != nil {
						hooks.OnSessionTerminated(client, ExpiredTermination)
					}
				}

				srv.statsManager.addSessionExpired()
				srv.statsManager.decSessionInactive()
			}
		}
	}
	srv.mu.Unlock()
}

// server event loop
func (srv *server) eventLoop() {
	if srv.config.SessionExpiryInterval != 0 {
		sessionExpireTimer := time.NewTicker(srv.config.SessionExpiryCheckInterval)
		defer sessionExpireTimer.Stop()
		for {
			select {
			case register := <-srv.register:
				srv.registerHandler(register)
			case unregister := <-srv.unregister:
				srv.unregisterHandler(unregister)
			case msg := <-srv.msgRouter:
				srv.msgRouterHandler(msg)
			case <-sessionExpireTimer.C:
				srv.sessionExpireCheck()
			}
		}
	} else {
		for {
			select {
			case register := <-srv.register:
				srv.registerHandler(register)
			case unregister := <-srv.unregister:
				srv.unregisterHandler(unregister)
			case msg := <-srv.msgRouter:
				srv.msgRouterHandler(msg)
			}
		}
	}
}

// NewServer returns a mqtt server instance with the given options
func NewServer(opts ...Options) Server {
	// statistics
	subStore := subscription_trie.NewStore()
	statsMgr := newStatsManager(subStore)
	srv := &server{
		status:          serverStatusInit,
		exitChan:        make(chan struct{}),
		clients:         make(map[string]*client),
		offlineClients:  make(map[string]time.Time),
		retainedDB:      retained_trie.NewStore(),
		subscriptionsDB: subStore,
		config:          DefaultConfig,
		statsManager:    statsMgr,
		hooks:           make([]Hooks, 0),
	}

	srv.publishService = &publishService{server: srv}

	for _, fn := range opts {
		fn(srv)
	}

	return srv
}

// Init initialises the options.
func (srv *server) Init(opts ...Options) {
	for _, fn := range opts {
		fn(srv)
	}
}

// Client returns the client for given clientID
func (srv *server) Client(clientID string) Client {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	return srv.clients[clientID]
}

func (srv *server) serveTCP(l net.Listener) {
	defer func() {
		l.Close()
	}()

	var tempDelay time.Duration
	for {
		conn, e := l.Accept()
		if e != nil {
			if ne, ok := e.(net.Error); ok && ne.Temporary() {
				if tempDelay == 0 {
					tempDelay = 5 * time.Millisecond
				} else {
					tempDelay *= 2
				}
				if max := 1 * time.Second; tempDelay > max {
					tempDelay = max
				}
				time.Sleep(tempDelay)
				continue
			}
			return
		}

		// tcp 建立连接时触发
		for _, hooks := range srv.hooks {
			if hooks.OnAccept != nil {
				if !hooks.OnAccept(conn) {
					conn.Close()
					continue
				}
			}
		}

		client := srv.newClient(conn)
		go client.serve()
	}
}

func (srv *server) newClient(c net.Conn) *client {
	client := &client{
		server:        srv,
		rwc:           c,
		bufr:          newBufioReaderSize(c, readBufferSize),
		bufw:          newBufioWriterSize(c, writeBufferSize),
		close:         make(chan struct{}),
		closeComplete: make(chan struct{}),
		error:         make(chan error, 1),
		in:            make(chan packets.Packet, readBufferSize),
		out:           make(chan packets.Packet, writeBufferSize),
		status:        Connecting,
		opts:          &options{},
		cleanWillFlag: false,
		ready:         make(chan struct{}),
		statsManager:  newSessionStatsManager(),
		userData:      make(map[string]nson.Value),
	}

	client.packetReader = packets.NewReader(client.bufr)
	client.packetWriter = packets.NewWriter(client.bufw)

	client.setConnecting()
	client.newSession()

	return client
}

func (srv *server) loadPlugins() error {
	for _, p := range srv.plugins {
		zaplog.Info("loading plugin", zap.String("name", p.Name()))
		err := p.Load(srv)
		if err != nil {
			return err
		}

		srv.hooks = append(srv.hooks, p.Hooks())
	}

	return nil
}

// Run starts the mqtt server. This method is non-blocking
func (srv *server) Run() {
	srv.msgRouter = make(chan *msgRouter, srv.config.MsgRouterLen)
	srv.register = make(chan *register, srv.config.RegisterLen)
	srv.unregister = make(chan *unregister, srv.config.UnregisterLen)

	var tcps []string
	var ws []string

	for _, v := range srv.tcpListener {
		tcps = append(tcps, v.Addr().String())
	}

	for _, v := range srv.websocketServer {
		ws = append(ws, v.Server.Addr)
	}

	zaplog.Info("starting mqtt server", zap.Strings("tcp server listen on", tcps), zap.Strings("websocket server listen on", ws))

	err := srv.loadPlugins()
	if err != nil {
		panic(err)
	}

	srv.status = serverStatusStarted

	go srv.eventLoop()

	for _, ln := range srv.tcpListener {
		go srv.serveTCP(ln)
	}

	for _, server := range srv.websocketServer {
		mux := http.NewServeMux()
		mux.Handle(server.Path, srv.wsHandler())
		server.Server.Handler = mux
		go srv.serveWebSocket(server)
	}
}

// Stop gracefully stops the mqtt server by the following steps:
//  1. Closing all open TCP listeners and shutting down all open websocket servers
//  2. Closing all idle connections
//  3. Waiting for all connections have been closed
//  4. Triggering OnStop()
func (srv *server) Stop(ctx context.Context) error {
	zaplog.Info("stopping mqtt server")

	defer func() {
		zaplog.Info("server stopped")
		//zaplog.Sync()
	}()

	select {
	case <-srv.exitChan:
		return nil
	default:
		close(srv.exitChan)
	}

	for _, l := range srv.tcpListener {
		l.Close()
	}
	for _, ws := range srv.websocketServer {
		ws.Server.Shutdown(ctx)
	}

	//关闭所有的client
	//closing all idle clients
	srv.mu.Lock()
	closeCompleteSet := make([]<-chan struct{}, len(srv.clients))
	i := 0
	for _, c := range srv.clients {
		closeCompleteSet[i] = c.Close()
		i++
	}
	srv.mu.Unlock()

	done := make(chan struct{})

	go func() {
		for _, v := range closeCompleteSet {
			//等所有的session退出完毕
			//waiting for all sessions to unregister
			<-v
		}
		close(done)
	}()

	select {
	case <-ctx.Done():
		zaplog.Warn("server stop timeout, forced exit", zap.String("error", ctx.Err().Error()))
		return ctx.Err()
	case <-done:
		// server 停止后触发
		for _, hooks := range srv.hooks {
			if hooks.OnStop != nil {
				hooks.OnStop()
			}
		}

		for _, v := range srv.plugins {
			err := v.Unload()
			if err != nil {
				zaplog.Warn("plugin unload error", zap.String("error", err.Error()))
			}
		}

		return nil
	}
}
