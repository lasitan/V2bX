package sing

import (
	"reflect"
	"unsafe"

	"github.com/sagernet/sing-box/adapter"
	singDNS "github.com/sagernet/sing-box/dns"
)

func reflectUnexportedField(rv reflect.Value, name string) reflect.Value {
	f := rv.FieldByName(name)
	if !f.IsValid() {
		return reflect.Value{}
	}
	return reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
}

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

// resetRuntimeDNSClient purges in-memory DNS client caches.
// It does not close DNS transports or user connections.
func resetRuntimeDNSClient(dnsRouter adapter.DNSRouter) {
	client := dnsClientFromRouter(dnsRouter)
	if client == nil {
		return
	}
	client.ClearCache()
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
