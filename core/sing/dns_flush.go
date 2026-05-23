package sing

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/sagernet/bbolt"
	bboltErrors "github.com/sagernet/bbolt/errors"
	"github.com/sagernet/sing-box/adapter"
	singDNS "github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/experimental/cachefile"
	"github.com/sagernet/sing/service"
)

const rdrcCacheBucket = "rdrc2"

type cachePurger interface {
	Purge()
}

// FlushDNSCache clears DNS resolution caches at runtime without restarting the core,
// closing DNS transports, or dropping existing user connections.
func (b *Sing) FlushDNSCache() error {
	if b.box == nil || b.ctx == nil {
		return errors.New("sing-box is not started")
	}
	dnsRouter := service.FromContext[adapter.DNSRouter](b.ctx)
	if dnsRouter == nil {
		return errors.New("dns router is not available")
	}

	var errs []error
	if err := b.resetFakeIPStores(); err != nil {
		errs = append(errs, err)
	}
	if err := b.clearPersistentDNSCache(); err != nil {
		errs = append(errs, err)
	}

	dnsRouter.ClearCache()
	clearDNSReverseMapping(dnsRouter)
	resetRuntimeDNSClient(b, dnsRouter)

	if b.logFactory != nil {
		b.logFactory.NewLogger("dns").Info("dns flush completed: LRU/RDRC/FakeIP cleared, force fresh lookup for ", dnsForceFreshDuration)
	}
	return errors.Join(errs...)
}

func clearDNSReverseMapping(dnsRouter adapter.DNSRouter) {
	router, ok := dnsRouter.(*singDNS.Router)
	if !ok {
		return
	}
	purgeUnexportedCacheField(reflect.ValueOf(router).Elem(), "dnsReverseMapping")
}

// clearPersistentDNSCache removes only DNS-related buckets in cache.db (FakeIP, RDRC).
// Other cache entries such as rule sets and selector state are kept.
func (b *Sing) clearPersistentDNSCache() error {
	if b.box == nil || b.ctx == nil {
		return nil
	}
	cacheFile := service.FromContext[adapter.CacheFile](b.ctx)
	if cacheFile == nil {
		return nil
	}
	var errs []error
	if err := resetFakeIPPersist(cacheFile); err != nil {
		errs = append(errs, fmt.Errorf("clear fakeip cache: %w", err))
	}
	if err := resetRDRCCache(cacheFile); err != nil {
		errs = append(errs, fmt.Errorf("clear rdrc cache: %w", err))
	}
	return errors.Join(errs...)
}

func resetFakeIPPersist(cacheFile adapter.CacheFile) error {
	_ = cacheFile.FakeIPReset()
	if cf, ok := cacheFile.(*cachefile.CacheFile); ok {
		clearCacheFileInMemory(cf)
	}
	return nil
}

func (b *Sing) resetFakeIPStores() error {
	transportManager := service.FromContext[adapter.DNSTransportManager](b.ctx)
	if transportManager == nil {
		return nil
	}
	var errs []error
	for _, transport := range transportManager.Transports() {
		fakeIPTransport, ok := transport.(adapter.FakeIPTransport)
		if !ok {
			continue
		}
		store := fakeIPTransport.Store()
		resetter, ok := store.(interface{ Reset() error })
		if !ok {
			continue
		}
		if err := resetter.Reset(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func resetRDRCCache(cacheFile adapter.CacheFile) error {
	cf, ok := cacheFile.(*cachefile.CacheFile)
	if !ok || cf.DB == nil {
		return nil
	}
	key := []byte(rdrcCacheBucket)
	err := cf.DB.Batch(func(tx *bbolt.Tx) error {
		if err := deleteCacheBucket(tx, key); err != nil {
			return err
		}
		return tx.ForEach(func(name []byte, bucket *bbolt.Bucket) error {
			if len(name) > 0 && name[0] == 0 && bucket != nil {
				_ = bucket.DeleteBucket(key)
			}
			return nil
		})
	})
	if err != nil {
		return err
	}
	clearCacheFileInMemory(cf)
	return nil
}

func deleteCacheBucket(tx *bbolt.Tx, key []byte) error {
	err := tx.DeleteBucket(key)
	if err != nil && !errors.Is(err, bboltErrors.ErrBucketNotFound) {
		return err
	}
	return nil
}
