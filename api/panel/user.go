package panel

import (
	"fmt"
	"encoding/json/v2"
)

type OnlineUser struct {
	UID int
	IP  string
}

type UserInfo struct {
	Id          int    `json:"id" msgpack:"id"`
	Uuid        string `json:"uuid" msgpack:"uuid"`
	SpeedLimit  int    `json:"speed_limit" msgpack:"speed_limit"`
	DeviceLimit int    `json:"device_limit" msgpack:"device_limit"`
}

type UserListBody struct {
	Users []UserInfo `json:"users" msgpack:"users"`
}

type AliveMap struct {
	Alive map[int]int `json:"alive"`
}

// GetUserList will pull user from v2board
func (c *Client) GetUserList() ([]UserInfo, error) {
	const path = "/api/v1/server/UniProxy/user"
	status, headers, body, _, err := c.doRequest("GET", path, map[string]string{
		"If-None-Match": c.userEtag,
		"Accept":        "application/json",
	}, nil)
	if status == 304 {
		return nil, nil
	}
	if err = c.checkResponseRaw(path, status, body, err); err != nil {
		return nil, err
	}
	userlist := &UserListBody{}
	if err := json.Unmarshal(body, userlist); err != nil {
		return nil, fmt.Errorf("decode user list error: %w", err)
	}
	if headers != nil {
		if v, ok := headers["ETag"]; ok && len(v) > 0 {
			c.userEtag = v[0]
		}
	}
	return userlist.Users, nil
}

// GetUserAlive will fetch the alive_ip count for users
func (c *Client) GetUserAlive() (map[int]int, error) {
	c.AliveMap = &AliveMap{}
	const path = "/api/v1/server/UniProxy/alivelist"
	status, _, body, _, err := c.doRequest("GET", path, map[string]string{
		"Accept": "application/json",
	}, nil)
	if err != nil || status >= 399 {
		c.AliveMap.Alive = make(map[int]int)
		return c.AliveMap.Alive, nil
	}
	if err := json.Unmarshal(body, c.AliveMap); err != nil {
		c.AliveMap.Alive = make(map[int]int)
	}

	return c.AliveMap.Alive, nil
}

type UserTraffic struct {
	UID      int
	Upload   int64
	Download int64
}

// ReportUserTraffic reports the user traffic
func (c *Client) ReportUserTraffic(userTraffic []UserTraffic) error {
	data := make(map[int][]int64, len(userTraffic))
	for i := range userTraffic {
		data[userTraffic[i].UID] = []int64{userTraffic[i].Upload, userTraffic[i].Download}
	}
	const path = "/api/v1/server/UniProxy/push"
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	status, _, respBody, _, err := c.doRequest("POST", path, map[string]string{
		"Content-Type": "application/json",
		"Accept":       "application/json",
	}, body)
	err = c.checkResponseRaw(path, status, respBody, err)
	if err != nil {
		return err
	}
	return nil
}

func (c *Client) ReportNodeOnlineUsers(data *map[int][]string) error {
	const path = "/api/v1/server/UniProxy/alive"
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	status, _, respBody, _, err := c.doRequest("POST", path, map[string]string{
		"Content-Type": "application/json",
		"Accept":       "application/json",
	}, body)
	err = c.checkResponseRaw(path, status, respBody, err)
	if err != nil {
		return nil
	}
	return nil
}
