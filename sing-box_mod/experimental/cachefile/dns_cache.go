package cachefile

import (
	"bytes"
	"encoding/binary"
	"time"

	"github.com/sagernet/bbolt"
	"github.com/sagernet/sing/common/buf"

	mDNS "github.com/miekg/dns"
)

var bucketDNSCache = []byte("dns_cache_v1")

func dnsCacheKey(transportName string, qName string, qType uint16, qClass uint16) []byte {
	key := buf.Get(4 + len(transportName) + 1 + len(qName))
	binary.BigEndian.PutUint16(key[0:2], qType)
	binary.BigEndian.PutUint16(key[2:4], qClass)
	copy(key[4:4+len(transportName)], transportName)
	key[4+len(transportName)] = '|'
	copy(key[5+len(transportName):], qName)
	return key
}

func (c *CacheFile) LoadDNSCache(transportName string, qName string, qType uint16, qClass uint16) (response *mDNS.Msg, expiresAt time.Time, loaded bool) {
	key := dnsCacheKey(transportName, qName, qType, qClass)
	defer buf.Put(key)
	_ = c.DB.View(func(tx *bbolt.Tx) error {
		bucket := c.bucket(tx, bucketDNSCache)
		if bucket == nil {
			return nil
		}
		value := bucket.Get(key)
		if len(value) < 12 {
			return nil
		}
		expireUnix := int64(binary.BigEndian.Uint64(value[0:8]))
		msgLen := int(binary.BigEndian.Uint32(value[8:12]))
		if msgLen <= 0 || len(value) < 12+msgLen {
			return nil
		}
		msg := &mDNS.Msg{}
		if err := msg.Unpack(value[12 : 12+msgLen]); err != nil {
			return nil
		}
		expiresAt = time.Unix(expireUnix, 0)
		response = msg
		loaded = true
		return nil
	})
	return
}

func (c *CacheFile) SaveDNSCache(transportName string, qName string, qType uint16, qClass uint16, response *mDNS.Msg, expiresAt time.Time) error {
	wire, err := response.Pack()
	if err != nil {
		return err
	}
	key := dnsCacheKey(transportName, qName, qType, qClass)
	defer buf.Put(key)
	var payload bytes.Buffer
	expireBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(expireBytes, uint64(expiresAt.Unix()))
	lenBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBytes, uint32(len(wire)))
	payload.Write(expireBytes)
	payload.Write(lenBytes)
	payload.Write(wire)
	return c.DB.Batch(func(tx *bbolt.Tx) error {
		bucket, err := c.createBucket(tx, bucketDNSCache)
		if err != nil {
			return err
		}
		return bucket.Put(key, payload.Bytes())
	})
}

func (c *CacheFile) DeleteDNSCache(transportName string, qName string, qType uint16, qClass uint16) error {
	key := dnsCacheKey(transportName, qName, qType, qClass)
	defer buf.Put(key)
	return c.DB.Batch(func(tx *bbolt.Tx) error {
		bucket := c.bucket(tx, bucketDNSCache)
		if bucket == nil {
			return nil
		}
		return bucket.Delete(key)
	})
}

func (c *CacheFile) ClearDNSCache() error {
	return c.DB.Batch(func(tx *bbolt.Tx) error {
		if c.cacheID == nil {
			_ = tx.DeleteBucket(bucketDNSCache)
			_, err := tx.CreateBucketIfNotExists(bucketDNSCache)
			return err
		}
		parent := tx.Bucket(c.cacheID)
		if parent == nil {
			return nil
		}
		_ = parent.DeleteBucket(bucketDNSCache)
		_, err := parent.CreateBucketIfNotExists(bucketDNSCache)
		return err
	})
}
