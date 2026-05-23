package sing

import (
	"errors"
	"fmt"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/service"
)

func (b *Sing) FlushDNSCache() error {
	if b.box == nil || b.ctx == nil {
		return errors.New("sing-box is not started")
	}
	dnsRouter := service.FromContext[adapter.DNSRouter](b.ctx)
	if dnsRouter == nil {
		return errors.New("dns router is not available")
	}
	dnsRouter.ClearCache()

	cacheFile := service.FromContext[adapter.CacheFile](b.ctx)
	if cacheFile != nil && cacheFile.StoreFakeIP() {
		if err := cacheFile.FakeIPReset(); err != nil {
			return fmt.Errorf("clear fakeip cache: %w", err)
		}
	}
	return nil
}
