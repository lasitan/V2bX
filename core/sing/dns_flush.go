package sing

import (
	"errors"
	"fmt"

	"github.com/sagernet/bbolt"
	bboltErrors "github.com/sagernet/bbolt/errors"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/experimental/cachefile"
	"github.com/sagernet/sing/service"
)

const rdrcCacheBucket = "rdrc2"

func (b *Sing) FlushDNSCache() error {
	if b.box == nil || b.ctx == nil {
		return errors.New("sing-box is not started")
	}
	dnsRouter := service.FromContext[adapter.DNSRouter](b.ctx)
	if dnsRouter == nil {
		return errors.New("dns router is not available")
	}
	dnsRouter.ClearCache()
	return b.clearPersistentDNSCache()
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
	if cacheFile.StoreFakeIP() {
		if err := cacheFile.FakeIPReset(); err != nil {
			errs = append(errs, fmt.Errorf("clear fakeip cache: %w", err))
		}
	}
	if err := resetRDRCCache(cacheFile); err != nil {
		errs = append(errs, fmt.Errorf("clear rdrc cache: %w", err))
	}
	return errors.Join(errs...)
}

func resetRDRCCache(cacheFile adapter.CacheFile) error {
	cf, ok := cacheFile.(*cachefile.CacheFile)
	if !ok || !cf.StoreRDRC() || cf.DB == nil {
		return nil
	}
	key := []byte(rdrcCacheBucket)
	return cf.DB.Batch(func(tx *bbolt.Tx) error {
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
}

func deleteCacheBucket(tx *bbolt.Tx, key []byte) error {
	err := tx.DeleteBucket(key)
	if err != nil && !errors.Is(err, bboltErrors.ErrBucketNotFound) {
		return err
	}
	return nil
}
