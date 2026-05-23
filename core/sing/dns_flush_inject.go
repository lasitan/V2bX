package sing

import (
	"reflect"

	"github.com/sagernet/sing-box/adapter"
	singDNS "github.com/sagernet/sing-box/dns"
)

func dnsClientFromRouter(dnsRouter adapter.DNSRouter) *singDNS.Client {
	router, ok := dnsRouter.(*singDNS.Router)
	if !ok {
		return nil
	}
	clientVal := reflect.ValueOf(router).Elem().FieldByName("client")
	if !clientVal.IsValid() || clientVal.IsNil() {
		return nil
	}
	client, ok := clientVal.Interface().(*singDNS.Client)
	if !ok {
		return nil
	}
	return client
}

// resetRuntimeDNSClient purges in-memory DNS client caches and rebinds RDRC.
// It does not close DNS transports or user connections.
func resetRuntimeDNSClient(dnsRouter adapter.DNSRouter) {
	resetDNSClientState(dnsClientFromRouter(dnsRouter))
}

func resetDNSClientState(client *singDNS.Client) {
	if client == nil {
		return
	}
	client.ClearCache()
	cv := reflect.ValueOf(client).Elem()
	purgeFieldCache(cv, "cache")
	purgeFieldCache(cv, "transportCache")

	rdrcField := cv.FieldByName("rdrc")
	if !rdrcField.IsValid() {
		return
	}
	rdrcField.Set(reflect.Zero(rdrcField.Type()))

	initRDRC := cv.FieldByName("initRDRCFunc")
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

func purgeFieldCache(cv reflect.Value, name string) {
	field := cv.FieldByName(name)
	if !field.IsValid() || field.IsNil() {
		return
	}
	if purger, ok := field.Interface().(cachePurger); ok {
		purger.Purge()
	}
}
