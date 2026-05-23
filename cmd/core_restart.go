package cmd

import (
	"github.com/InazumaV/V2bX/conf"
	vCore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/node"
	log "github.com/sirupsen/logrus"
)

func restartCoreOnly(c *conf.Conf, vc *vCore.Core, nodes *node.Node) error {
	newCore, err := nodes.WithCoreRestart(func() (vCore.Core, error) {
		if *vc != nil {
			if closeErr := (*vc).Close(); closeErr != nil {
				log.WithField("err", closeErr).Warn("close old core before restart")
			}
		}
		next, err := vCore.NewCore(c.CoresConfig)
		if err != nil {
			return nil, err
		}
		if err = next.Start(); err != nil {
			_ = next.Close()
			return nil, err
		}
		return next, nil
	})
	if err != nil {
		return err
	}
	*vc = newCore
	return nil
}
