package network

// AddressFormat identifies the address encoding family of a network. It is
// derived from the CAIP-2 namespace — never stored as a column, so the
// registry cannot hold a namespace/format pair that disagrees (plan D1).
// Known limitation: namespace↔format is not universally 1:1 (bip122 covers
// multiple encodings); unknown namespaces fail closed to "".
type AddressFormat string

const (
	AddressFormatEVM    AddressFormat = "evm"
	AddressFormatSolana AddressFormat = "solana"
	AddressFormatTron   AddressFormat = "tron"
)

// AddressFormatForNamespace maps a CAIP-2 namespace to its address format.
// Unrecognized namespaces return "" — callers must fail closed.
func AddressFormatForNamespace(namespace string) AddressFormat {
	switch namespace {
	case "eip155":
		return AddressFormatEVM
	case "solana":
		return AddressFormatSolana
	case "tron":
		return AddressFormatTron
	default:
		return ""
	}
}

// AddressFormat returns the address encoding family implied by the
// network's namespace; zero-value-safe ("" for unknown namespaces).
func (n Network) AddressFormat() AddressFormat {
	return AddressFormatForNamespace(n.Namespace)
}
