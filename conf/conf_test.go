package conf

import (
	"os"
	"testing"
	"time"
)

func TestConf_LoadFromPath(t *testing.T) {
	c := New()
	t.Log(c.LoadFromPath("../example/config.json"), c.NodeConfig)
}

func TestConf_Watch(t *testing.T) {
	c := New()
	f, err := os.CreateTemp("", "v2bx_conf_watch_*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	_ = f.Close()
	if err := c.Watch(f.Name(), "", "", func() {}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
}
