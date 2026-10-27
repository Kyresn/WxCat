// Package wcf is a minimal WeChatFerry RPC client.
//
// The protobuf bindings in wcf.pb.go are vendored from lich0821/WeChatFerry
// (MIT). The RPC layer below is a trimmed rewrite of the upstream Go client:
// it removes the logging dependency, returns errors instead of swallowing
// them, and makes the receive loop cancellable.
package wcf

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.nanomsg.org/mangos/v3"
	"go.nanomsg.org/mangos/v3/protocol"
	"go.nanomsg.org/mangos/v3/protocol/pair1"
	_ "go.nanomsg.org/mangos/v3/transport/all"
	"google.golang.org/protobuf/proto"
)

const defaultTimeout = 15 * time.Second

// Client speaks the WeChatFerry pair1 RPC protocol.
type Client struct {
	cmdAddr string
	msgAddr string

	mu     sync.Mutex
	socket protocol.Socket
	recv   bool
}

// New connects to the command port.
func New(cmdAddr, msgAddr string) (*Client, error) {
	if cmdAddr == "" {
		cmdAddr = "tcp://127.0.0.1:10086"
	}
	if msgAddr == "" {
		msgAddr = "tcp://127.0.0.1:10087"
	}
	c := &Client{cmdAddr: cmdAddr, msgAddr: msgAddr}
	if err := c.connect(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) connect() error {
	socket, err := pair1.NewSocket()
	if err != nil {
		return fmt.Errorf("create wcf socket: %w", err)
	}
	_ = socket.SetOption(mangos.OptionRecvDeadline, defaultTimeout)
	_ = socket.SetOption(mangos.OptionSendDeadline, defaultTimeout)
	if err := socket.Dial(c.cmdAddr); err != nil {
		return fmt.Errorf("dial wcf %s: %w", c.cmdAddr, err)
	}
	c.mu.Lock()
	c.socket = socket
	c.mu.Unlock()
	return nil
}

// Close disables message reception and closes the RPC socket.
func (c *Client) Close() error {
	if c.recv {
		_, _ = c.EnableRecv(false)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.socket == nil {
		return nil
	}
	err := c.socket.Close()
	c.socket = nil
	return err
}

func (c *Client) call(fn Functions, msg isRequest_Msg) (*Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.socket == nil {
		return nil, errors.New("wcf client is closed")
	}
	req := &Request{Func: fn}
	if msg != nil {
		req.Msg = msg
	}
	data, err := proto.Marshal(req)
	if err != nil {
		return nil, err
	}
	if err := c.socket.Send(data); err != nil {
		return nil, fmt.Errorf("wcf send %s: %w", fn.String(), err)
	}
	raw, err := c.socket.Recv()
	if err != nil {
		return nil, fmt.Errorf("wcf recv %s: %w", fn.String(), err)
	}
	resp := &Response{}
	if err := proto.Unmarshal(raw, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// IsLogin reports whether WeChat is logged in.
func (c *Client) IsLogin() (bool, error) {
	resp, err := c.call(Functions_FUNC_IS_LOGIN, nil)
	if err != nil {
		return false, err
	}
	return resp.GetStatus() == 1, nil
}

// SelfWXID returns the logged-in wxid.
func (c *Client) SelfWXID() (string, error) {
	resp, err := c.call(Functions_FUNC_GET_SELF_WXID, nil)
	if err != nil {
		return "", err
	}
	return resp.GetStr(), nil
}

// UserInfo returns the logged-in account profile.
func (c *Client) UserInfo() (*UserInfo, error) {
	resp, err := c.call(Functions_FUNC_GET_USER_INFO, nil)
	if err != nil {
		return nil, err
	}
	return resp.GetUi(), nil
}

// Contacts returns friends and groups.
func (c *Client) Contacts() ([]*RpcContact, error) {
	resp, err := c.call(Functions_FUNC_GET_CONTACTS, nil)
	if err != nil {
		return nil, err
	}
	return resp.GetContacts().GetContacts(), nil
}

// DBNames lists the databases exposed by the hook.
func (c *Client) DBNames() ([]string, error) {
	resp, err := c.call(Functions_FUNC_GET_DB_NAMES, nil)
	if err != nil {
		return nil, err
	}
	return resp.GetDbs().GetNames(), nil
}

// DBTables lists tables of one database.
func (c *Client) DBTables(db string) ([]*DbTable, error) {
	resp, err := c.call(Functions_FUNC_GET_DB_TABLES, &Request_Str{Str: db})
	if err != nil {
		return nil, err
	}
	return resp.GetTables().GetTables(), nil
}

// Query runs a read-only SQL query.
func (c *Client) Query(db, sql string) ([]*DbRow, error) {
	resp, err := c.call(Functions_FUNC_EXEC_DB_QUERY, &Request_Query{
		Query: &DbQuery{Db: db, Sql: sql},
	})
	if err != nil {
		return nil, err
	}
	return resp.GetRows().GetRows(), nil
}

// SendTxt sends a text message. ates lists wxids to @ in a group chat.
func (c *Client) SendTxt(text, receiver string, ates []string) (int32, error) {
	resp, err := c.call(Functions_FUNC_SEND_TXT, &Request_Txt{
		Txt: &TextMsg{Msg: text, Receiver: receiver, Aters: strings.Join(ates, ",")},
	})
	if err != nil {
		return 0, err
	}
	return resp.GetStatus(), nil
}

// SendIMG sends an image from an absolute path.
func (c *Client) SendIMG(path, receiver string) (int32, error) {
	resp, err := c.call(Functions_FUNC_SEND_IMG, &Request_File{
		File: &PathMsg{Path: path, Receiver: receiver},
	})
	if err != nil {
		return 0, err
	}
	return resp.GetStatus(), nil
}

// SendFile sends a file from an absolute path.
func (c *Client) SendFile(path, receiver string) (int32, error) {
	resp, err := c.call(Functions_FUNC_SEND_FILE, &Request_File{
		File: &PathMsg{Path: path, Receiver: receiver},
	})
	if err != nil {
		return 0, err
	}
	return resp.GetStatus(), nil
}

// SendEmotion sends a sticker from an absolute path.
func (c *Client) SendEmotion(path, receiver string) (int32, error) {
	resp, err := c.call(Functions_FUNC_SEND_EMOTION, &Request_File{
		File: &PathMsg{Path: path, Receiver: receiver},
	})
	if err != nil {
		return 0, err
	}
	return resp.GetStatus(), nil
}

// DecryptImage decrypts an encrypted image file into dst.
func (c *Client) DecryptImage(src, dst string) (int32, error) {
	resp, err := c.call(Functions_FUNC_DECRYPT_IMAGE, &Request_Dec{
		Dec: &DecPath{Src: src, Dst: dst},
	})
	if err != nil {
		return 0, err
	}
	return resp.GetStatus(), nil
}

// EnableRecv turns text message reception on or off.
func (c *Client) EnableRecv(enable bool) (int32, error) {
	fn := Functions_FUNC_ENABLE_RECV_TXT
	if !enable {
		fn = Functions_FUNC_DISABLE_RECV_TXT
	}
	resp, err := c.call(fn, &Request_Flag{Flag: enable})
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	c.recv = enable
	c.mu.Unlock()
	return resp.GetStatus(), nil
}

// OnMSG blocks and delivers incoming messages until ctx is cancelled or the
// socket fails.
func (c *Client) OnMSG(ctx context.Context, handler func(*WxMsg)) error {
	socket, err := pair1.NewSocket()
	if err != nil {
		return err
	}
	_ = socket.SetOption(mangos.OptionRecvDeadline, time.Second)
	_ = socket.SetOption(mangos.OptionSendDeadline, time.Second)
	if err := socket.Dial(c.msgAddr); err != nil {
		return fmt.Errorf("dial wcf message port %s: %w", c.msgAddr, err)
	}
	defer socket.Close()

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		raw, err := socket.Recv()
		if err != nil {
			if errors.Is(err, mangos.ErrRecvTimeout) {
				continue
			}
			return err
		}
		resp := &Response{}
		if err := proto.Unmarshal(raw, resp); err != nil {
			continue
		}
		if msg := resp.GetWxmsg(); msg != nil {
			handler(msg)
		}
	}
}
