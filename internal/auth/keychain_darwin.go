package auth

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>
#include <stdlib.h>
static OSStatus put(const char *key,const char *value) {
 CFStringRef service=CFSTR("co.nggalek.titen");
 CFStringRef account=CFStringCreateWithCString(NULL,key,kCFStringEncodingUTF8);
 CFDataRef data=CFDataCreate(NULL,(const UInt8*)value,strlen(value));
 const void *keys[]={kSecClass,kSecAttrService,kSecAttrAccount};
 const void *vals[]={kSecClassGenericPassword,service,account};
 CFDictionaryRef query=CFDictionaryCreate(NULL,keys,vals,3,&kCFTypeDictionaryKeyCallBacks,&kCFTypeDictionaryValueCallBacks);
 const void *uk[]={kSecValueData};const void *uv[]={data};
 CFDictionaryRef update=CFDictionaryCreate(NULL,uk,uv,1,&kCFTypeDictionaryKeyCallBacks,&kCFTypeDictionaryValueCallBacks);
 OSStatus status=SecItemUpdate(query,update);
 if(status==errSecItemNotFound){CFMutableDictionaryRef item=CFDictionaryCreateMutableCopy(NULL,0,query);CFDictionarySetValue(item,kSecValueData,data);CFDictionarySetValue(item,kSecAttrAccessible,kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly);status=SecItemAdd(item,NULL);CFRelease(item);}
 CFRelease(query);CFRelease(update);CFRelease(account);CFRelease(data);return status;
}
static OSStatus get(const char *key,CFDataRef *result){CFStringRef account=CFStringCreateWithCString(NULL,key,kCFStringEncodingUTF8);const void *keys[]={kSecClass,kSecAttrService,kSecAttrAccount,kSecReturnData,kSecMatchLimit};const void *vals[]={kSecClassGenericPassword,CFSTR("co.nggalek.titen"),account,kCFBooleanTrue,kSecMatchLimitOne};CFDictionaryRef query=CFDictionaryCreate(NULL,keys,vals,5,&kCFTypeDictionaryKeyCallBacks,&kCFTypeDictionaryValueCallBacks);OSStatus status=SecItemCopyMatching(query,(CFTypeRef*)result);CFRelease(query);CFRelease(account);return status;}
*/
import "C"
import (
	"fmt"
	"unsafe"
)

func SetSecret(key, value string) error {
	k := C.CString(key)
	v := C.CString(value)
	defer C.free(unsafe.Pointer(k))
	defer C.free(unsafe.Pointer(v))
	if s := C.put(k, v); s != 0 {
		return fmt.Errorf("Keychain save failed (%d)", s)
	}
	return nil
}
func GetSecret(key string) (string, error) {
	k := C.CString(key)
	defer C.free(unsafe.Pointer(k))
	var data C.CFDataRef
	if s := C.get(k, &data); s != 0 {
		return "", fmt.Errorf("Keychain lookup failed (%d)", s)
	}
	defer C.CFRelease(C.CFTypeRef(data))
	return string(C.GoBytes(unsafe.Pointer(C.CFDataGetBytePtr(data)), C.int(C.CFDataGetLength(data)))), nil
}
