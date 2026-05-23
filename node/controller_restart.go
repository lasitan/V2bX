package node

import (
	"fmt"

	vCore "github.com/InazumaV/V2bX/core"
	log "github.com/sirupsen/logrus"
)

// detachFromCore removes the node from the current core without stopping panel tasks or limiters.
func (c *Controller) detachFromCore() error {
	if c.tag == "" {
		return nil
	}
	c.flushCurrentTrafficToCache("core_detach")
	if err := c.server.DelNode(c.tag); err != nil {
		return fmt.Errorf("detach node %s: %w", c.tag, err)
	}
	log.WithField("tag", c.tag).Info("node detached from core for restart")
	return nil
}

// attachToCore registers the node and users on a new core instance.
func (c *Controller) attachToCore(server vCore.Core) error {
	if c.tag == "" || c.info == nil {
		c.server = server
		return nil
	}
	c.server = server
	if err := c.server.AddNode(c.tag, c.info, c.Options); err != nil {
		return fmt.Errorf("attach node %s: %w", c.tag, err)
	}
	added, err := c.server.AddUsers(&vCore.AddUsersParams{
		Tag:      c.tag,
		Users:    c.userList,
		NodeInfo: c.info,
	})
	if err != nil {
		return fmt.Errorf("attach users %s: %w", c.tag, err)
	}
	log.WithFields(log.Fields{
		"tag":   c.tag,
		"users": added,
	}).Info("node reattached to core after restart")
	return nil
}
