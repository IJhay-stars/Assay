package sac

import (
	"errors"
	"testing"
)

const testnet = "Test SDF Network ; September 2015"

// The testnet SAC addresses the ten attested assets are keyed on, exactly as
// docs/deployment.md records them (derived there with
// `stellar contract id asset --network testnet`).
var testnetVectors = []struct {
	code, issuer, want string
}{
	{"AQUA", "GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA", "CDJF2JQINO7WRFXB2AAHLONFDPPI4M3W2UM5THGQQ7JMJDIEJYC4CMPG"},
	{"USDC", "GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN", "CA2E53VHFZ6YSWQIEIPBXJQGT6VW3VKWWZO555XKRQXYJ63GEBJJGHY7"},
	{"USDZ", "GAKTLPC4ZV37SSCITQ5IS5AQ4WPF4CF4VZJQPPAROSGXMYOATF5U6XPR", "CAOM5NKBTSGEXTZZKH3STWSFWURMODC3TZ4NS2THN7W5YUDFK3IOHIHU"},
	{"BERKSHIRE", "GA22QHSHQEHDJS2ZOINSC77XPPQ24G5EFRJGVEIZLKC5FAW3PQ5XNSDQ", "CALAZXOC32XF2DFRKDOFQK7XQETZZTUK7C2OKC6YKPX6KY2YWU2YSPQW"},
	{"ARST", "GCSAZVWXZKWS4XS223M5F54H2B6XPIIXZZGP7KEAIU6YSL5HDRGCI3DG", "CBARCMJYRRNSYCWCR3EU2PEHAHWHBCQSMIKQIUSDWR3BK7CBCP622Q2R"},
	{"USDGLO", "GBBS25EGYQPGEZCGCFBKG4OAGFXU6DSOQBGTHELLJT3HZXZJ34HWS6XV", "CDGBOKCE25PVUKFWST2EEK52NHRS5WQ7TN26DFJYCEQNZNROQRSPIBQA"},
	{"DOGE", "GA22IDJNHUMC3XKUCCBFNTQIJOUBWINC5GCXHLJ2V6KZ3OWAXCULNQ7P", "CDUV37BUTYKKWNGECZZNRYMM7JIQYYWAI7L2TPTXWQAEMIPG4SXRBRPD"},
	{"VELO", "GDM4RQUQQUVSKQA7S6EM7XBZP3FCGH4Q7CL6TABQ7B2BEJ5ERARM2M5M", "CDHI6B6HBY2Q7BVGQ74C23GN3TKPGLCCMOSSRTEQDTTX4ELO5V6A6IOM"},
	{"REPO", "GA2BVQLGAG6UJDPHHTJDQAPBUYJV7D6IWIISGJI2LMQXTOKMHO36EAZR", "CBLZSCLVU3ZXRUJ3GEL7R3IP6CSN6VUEWPX7O4OYS3F3BYFUCJR7EEAA"},
	{"KALE", "GAKJM27QTNLBBZ352HQ4IDR3GWXUXQUEBKBDWOJG7RBH2NVEWUNSYCEF", "CCVNR6CGD6NFQG7XC6AVU5HF4YAPVXW5KZJCD73XFKUUO4YZUNEFKU2Z"},
}

func TestAddressMatchesDeploymentTable(t *testing.T) {
	for _, v := range testnetVectors {
		got, err := Address(v.code, v.issuer, testnet)
		if err != nil {
			t.Fatalf("%s: %v", v.code, err)
		}
		if got != v.want {
			t.Errorf("%s-%s on testnet = %s, want %s", v.code, v.issuer, got, v.want)
		}
	}
}

// The fail-closed control in docs/attestation-run.md: native XLM's testnet SAC.
func TestNativeMatchesRecordedControl(t *testing.T) {
	const want = "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC"
	if got := Native(testnet); got != want {
		t.Fatalf("native testnet SAC = %s, want %s", got, want)
	}
}

func TestAddressIsNetworkScoped(t *testing.T) {
	v := testnetVectors[0]
	pubnet, err := Address(v.code, v.issuer, "Public Global Stellar Network ; September 2015")
	if err != nil {
		t.Fatal(err)
	}
	if pubnet == v.want {
		t.Fatal("pubnet and testnet derived the same address; the network must scope the derivation")
	}
}

func TestAddressRejectsBadInput(t *testing.T) {
	issuer := testnetVectors[0].issuer
	for name, tc := range map[string]struct{ code, issuer string }{
		"empty code":         {"", issuer},
		"code too long":      {"ABCDEFGHIJKLM", issuer},
		"non-alphanumeric":   {"US-D", issuer},
		"contract as issuer": {"AQUA", testnetVectors[0].want},
		"bad checksum":       {"AQUA", issuer[:55] + "B"},
		"lowercase strkey":   {"AQUA", "g" + issuer[1:]},
		"short issuer":       {"AQUA", issuer[:40]},
	} {
		if _, err := Address(tc.code, tc.issuer, testnet); !errors.Is(err, ErrInvalidAsset) {
			t.Errorf("%s: err = %v, want ErrInvalidAsset", name, err)
		}
	}
}
