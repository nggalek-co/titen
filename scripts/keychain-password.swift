import Foundation
import Security

func fail(_ message: String) -> Never {
    FileHandle.standardError.write(Data((message + "\n").utf8))
    exit(1)
}

let arguments = Array(CommandLine.arguments.dropFirst())
guard arguments.count == 3 else {
    fail("Usage: keychain-password.swift get|set SERVICE ACCOUNT")
}

let mode = arguments[0]
let service = arguments[1]
let account = arguments[2]
let query: [String: Any] = [
    kSecClass as String: kSecClassGenericPassword,
    kSecAttrService as String: service,
    kSecAttrAccount as String: account,
    kSecAttrSynchronizable as String: kCFBooleanFalse as Any,
]

switch mode {
case "get":
    var lookup = query
    lookup[kSecReturnData as String] = true
    lookup[kSecMatchLimit as String] = kSecMatchLimitOne
    var result: CFTypeRef?
    let status = SecItemCopyMatching(lookup as CFDictionary, &result)
    guard status == errSecSuccess, let value = result as? Data else {
        fail("Signing passphrase is not available in the login Keychain (status \(status)).")
    }
    FileHandle.standardOutput.write(value)
case "set":
    let value = FileHandle.standardInput.readDataToEndOfFile()
    guard !value.isEmpty else { fail("Refusing to store an empty signing passphrase.") }
    let attributes: [String: Any] = [
        kSecValueData as String: value,
        kSecAttrAccessible as String: kSecAttrAccessibleWhenUnlockedThisDeviceOnly,
    ]
    let updateStatus = SecItemUpdate(query as CFDictionary, attributes as CFDictionary)
    if updateStatus == errSecItemNotFound {
        var item = query
        for (key, value) in attributes { item[key] = value }
        let addStatus = SecItemAdd(item as CFDictionary, nil)
        guard addStatus == errSecSuccess else {
            fail("Could not store the signing passphrase in the login Keychain (status \(addStatus)).")
        }
    } else if updateStatus != errSecSuccess {
        fail("Could not update the signing passphrase in the login Keychain (status \(updateStatus)).")
    }
default:
    fail("Mode must be get or set.")
}
