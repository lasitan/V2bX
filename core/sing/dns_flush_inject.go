package sing

import (
	"context"
	"reflect"
	"time"

	"github.com/sagernet/sing-box/adapter"
	singDNS "github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing/service"
)

const dnsForceFreshDuration = 30 * time.Second

func dnsClientFromRouter(dnsRouter adapter.DNSRouter) *singDNS.Client {
	router, ok := dnsRouter.(*singDNS.Router)
	if !ok {
		return nil
	}
	clientField := reflectUnexportedField(reflect.ValueOf(router).Elem(), "client")
	if !clientField.IsValid() || clientField.IsNil() {
		return nil
	}
	dnsClient, ok := clientField.Interface().(adapter.DNSClient)
	if !ok {
		return nil
	}
	client, ok := dnsClient.(*singDNS.Client)
	if !ok {
		return nil
	}
	return client
}

func resetRuntimeDNSClient(b *Sing, dnsRouter adapter.DNSRouter) {
	client := dnsClientFromRouter(dnsRouter)
	if client == nil {
		return
	}
	resetDNSClientState(client)
	b.beginDNSForceFresh(client)
}

func resetDNSClientState(client *singDNS.Client) {
	if client == nil {
		return
	}
	client.ClearCache()
	cv := reflect.ValueOf(client).Elem()
	purgeUnexportedCacheField(cv, "cache")
	purgeUnexportedCacheField(cv, "transportCache")

	rdrcField := reflectUnexportedField(cv, "rdrc")
	if !rdrcField.IsValid() {
		return
	}
	rdrcField.Set(reflect.Zero(rdrcField.Type()))

	initRDRC := reflectUnexportedField(cv, "initRDRCFunc")
	if !initRDRC.IsValid() || initRDRC.IsNil() {
		return
	}
	fn, ok := initRDRC.Interface().(func() adapter.RDRCStore)
	if !ok || fn == nil {
		return
	}
	if store := fn(); store != nil {
		rdrcField.Set(reflect.ValueOf(store))
	}
}

func purgeUnexportedCacheField(rv reflect.Value, name string) {
	field := reflectUnexportedField(rv, name)
	if !field.IsValid() || field.IsNil() {
		return
	}
	if purger, ok := field.Interface().(cachePurger); ok {
		purger.Purge()
	}
}

func (b *Sing) beginDNSForceFresh(client *singDNS.Client) {
	if client == nil {
		return
	}
	cv := reflect.ValueOf(client).Elem()
	disableField := reflectUnexportedField(cv, "disableCache")
	if !disableField.IsValid() {
		return
	}

	b.dnsBypassMu.Lock()
	if b.dnsBypassCancel != nil {
		b.dnsBypassCancel()
	}
	if !b.dnsBypassCaptured {
		b.dnsCacheDisabledDefault = disableField.Bool()
		b.dnsBypassCaptured = true
	}
	b.dnsBypassGen++
	gen := b.dnsBypassGen
	ctx, cancel := context.WithCancel(context.Background())
	b.dnsBypassCancel = cancel
	b.dnsBypassMu.Unlock()

	disableField.SetBool(true)

	go func() {
		select {
		case <-ctx.Done():
		case <-time.After(dnsForceFreshDuration):
		}
		b.dnsBypassMu.Lock()
		if b.dnsBypassGen == gen {
			disableField.SetBool(b.dnsCacheDisabledDefault)
			b.dnsBypassCancel = nil
		}
		b.dnsBypassMu.Unlock()
	}()
}

func (b *Sing) stopDNSBypass() {
	b.dnsBypassMu.Lock()
	defer b.dnsBypassMu.Unlock()
	if b.dnsBypassCancel != nil {
		b.dnsBypassCancel()
		b.dnsBypassCancel = nil
	}
	b.dnsBypassGen++
	client := dnsClientFromRouter(service.FromContext[adapter.DNSRouter](b.ctx))
	if client == nil {
		return
	}
	disableField := reflectUnexportedField(reflect.ValueOf(client).Elem(), "disableCache")
	if disableField.IsValid() {
		disableField.SetBool(b.dnsCacheDisabledDefault)
	}
}
