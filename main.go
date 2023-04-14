package main

/*
心跳（通过Ping/Pong消息），多房间（通过将房间名作为URL参数传递）以及限制发言时间（通过设置读写超时）time。
并发问题用锁解决，可能解决的不是好但是能用
*/

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// Message 代表一个从客户端发送到服务器的消息
type Message struct {
	Room string `json:"room"`
	Data string `json:"data"`
}

// Client 代表一个连接到服务器的客户端
type Client struct {
	conn *websocket.Conn
	send chan Message
}

// Room 代表一个聊天室
type Room struct {
	clients    map[*Client]bool
	broadcast  chan Message
	register   chan *Client
	unregister chan *Client
}

// 升级
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// 建立聊天室
var rooms = make(map[string]*Room)

// 来个读写锁
var roomLock sync.RWMutex

func main() {
	r := gin.Default()

	r.GET("/talk", func(c *gin.Context) {
		roomName := c.Param("room")                             //房间号名
		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil) //升级
		//错误检查

		if err != nil {
			log.Printf("Failed to set websocket upgrade: %+v", err)
			return
		}

		client := &Client{conn: conn, send: make(chan Message)} //建立链接

		roomLock.Lock()
		room, exists := rooms[roomName]
		//房间名不在就创建，在就不创建
		if !exists {
			room = createRoom()
			rooms[roomName] = room
			go room.run()
		}
		roomLock.Unlock()

		room.register <- client
		go client.writePump(room)
		go client.readPump(room)
	})

	r.Run(":8080")
}

func createRoom() *Room {
	return &Room{
		broadcast:  make(chan Message),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		clients:    make(map[*Client]bool),
	}
}
//读
func (c *Client) readPump(room *Room) {
	defer func() {
		room.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadLimit(512)
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error { c.conn.SetReadDeadline(time.Now().Add(60 * time.Second)); return nil })

	for {
		var msg Message
		err := c.conn.ReadJSON(&msg)
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			break
		}

		room.broadcast <- msg
	}
}
//写
func (c *Client) writePump(room *Room) {
	ticker := time.NewTicker(50 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				// 服务器关闭连接
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			err := c.conn.WriteJSON(message)
			if err != nil {
				log.Printf("error: %v", err)
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

//room的接口

func (r *Room) run() {
	for {
		select {
		case client := <-r.register:
			r.clients[client] = true
		case client := <-r.unregister:
			if _, ok := r.clients[client]; ok {
				delete(r.clients, client)
				close(client.send)
			}
		case message := <-r.broadcast:
			for client := range r.clients {
				select {
				case client.send <- message:
				default:
					close(client.send)
					delete(r.clients, client)
				}
			}
		}
	}
}
