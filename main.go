package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/cloudflare/circl/kem/kyber/kyber512"
	"github.com/hashicorp/vault/api"
	"github.com/supabase-community/supabase-go"
)

const (
	vaultAddr       = "http://127.0.0.1:8200"                                                                                                                                                                                            // Replace with your Vault address
	vaultToken      = "hvs.nfkLC6SezsV4t9Jw0GM2Vqcy"                                                                                                                                                                                     // Replace with your Vault token
	supabaseURL     = "https://uazjeoebwrxnlxzlozax.supabase.co"                                                                                                                                                                         // Replace with your Supabase project URL
	supabaseKey     = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJzdXBhYmFzZSIsInJlZiI6InVhemplb2Vid3J4bmx4emxvemF4Iiwicm9sZSI6ImFub24iLCJpYXQiOjE3NTI0ODM5NTYsImV4cCI6MjA2ODA1OTk1Nn0.cuTmZzA0vvIwRgVTq9USFjEbWhUgu5ijMxf1iTz1XrY" // Replace with your Supabase anon key
	pinataAPIKey    = "6cfd9ccc22fb716f9aef"                                                                                                                                                                                             // Replace with your Pinata API Key
	pinataAPISecret = "16f3aa4b7e43928001754324f5a5e8323719aa2502e4f5bed83f7c8a313d7d81"                                                                                                                                                 // Replace with your Pinata API Secret
)

func main() {
	generateFlag := flag.Int("generate", 0, "Generate Kyber key pair with given ID")
	encryptFlag := flag.String("encrypt", "", "String to encrypt with given symmetric key")
	sharedSecretFlag := flag.String("secret", "", "Symmetric key for encryption")
	distributeFlag := flag.String("distribute", "", "Ciphertext to distribute to IPFS")
	countFlag := flag.Int("count", 0, "Number of chunks for distribution")
	encapsulateFlag := flag.Bool("encapsulate", false, "Generate shared secret and deploy to Supabase")
	useKeyFlag := flag.Int("use_key", 0, "HashiCorp Vault Key ID")
	rowFlag := flag.Int("row", 0, "The ID of the row stored in Supabase")
	decryptFlag := flag.Bool("decrypt", false, "Decrypt the data from Supabase")
	// decryptKeyFlag := flag.Int("decrypt_key", 0, "Key ID for decryption")
	flag.Parse()

	switch {
	case *generateFlag > 0:
		generateKeyPair(*generateFlag)
	case *encryptFlag != "" && *sharedSecretFlag != "":
		encryptString(*encryptFlag, *sharedSecretFlag)
	case *distributeFlag != "" && *countFlag > 0:
		distributeCiphertext(*distributeFlag, *countFlag, *rowFlag)
	case *encapsulateFlag && *useKeyFlag > 0:
		generateSharedSecret(*useKeyFlag)
	case *decryptFlag && *rowFlag > 0 && *useKeyFlag > 0:
		decryptFromSupabase(*rowFlag, *useKeyFlag)
	default:
		fmt.Println("Invalid or missing flags. Usage: --generate, --encrypt, --distribute, --encapsulate, --decrypt")
	}
}

// Key generation and vault storage
func generateKeyPair(keyID int) {
	// Generate Kyber512 key pair
	scheme := kyber512.Scheme()
	publicKey, privateKey, err := scheme.GenerateKeyPair()
	if err != nil {
		fmt.Printf("Error generating Kyber key pair: %v\n", err)
		return
	}

	// Encode keys to base64 for storage
	pubKeyBytes, _ := publicKey.MarshalBinary()
	privKeyBytes, _ := privateKey.MarshalBinary()
	pubKeyB64 := base64.StdEncoding.EncodeToString(pubKeyBytes)
	privKeyB64 := base64.StdEncoding.EncodeToString(privKeyBytes)

	// Store in vault
	client, err := api.NewClient(&api.Config{Address: vaultAddr})
	if err != nil {
		fmt.Printf("Error connecting to Vault: %v\n", err)
		return
	}
	client.SetToken(vaultToken)

	data := map[string]interface{}{
		"public_key":  pubKeyB64,
		"private_key": privKeyB64,
		"key_id":      keyID,
	}
	_, err = client.Logical().Write(fmt.Sprintf("secret/data/key/%d", keyID), map[string]interface{}{"data": data})
	if err != nil {
		fmt.Printf("Error storing key in Vault: %v\n", err)
		return
	}
	fmt.Printf("Key pair with ID %d stored in Vault\n", keyID)
}

// Encrypt string with age
func encryptString(plaintext string, symKey string) {

	var recipient age.Recipient
	var err error

	recipient, err = age.NewScryptRecipient(symKey)

	if err != nil {
		fmt.Printf("Error parsing recipient: %v\n", err)
		return
	}

	var out bytes.Buffer
	w, err := age.Encrypt(&out, recipient)
	if err != nil {
		fmt.Printf("Error initializing encryption: %v\n", err)
		return
	}
	_, err = w.Write([]byte(plaintext))
	if err != nil {
		fmt.Printf("Error encrypting: %v\n", err)
		return
	}
	w.Close()

	fmt.Printf("Encrypted ciphertext: %s\n", base64.StdEncoding.EncodeToString(out.Bytes()))
}

// Distribute ciphertext to IPFS
func uploadToPinata(data string, apiKey, apiSecret string) (string, error) {
	url := "https://api.pinata.cloud/pinning/pinFileToIPFS"
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "chunk.bin")
	if err != nil {
		return "", err
	}
	_, err = part.Write([]byte(data))
	if err != nil {
		return "", err
	}
	writer.Close()

	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		return "", err
	}
	req.Header.Set("pinata_api_key", pinataAPIKey)
	req.Header.Set("pinata_secret_api_key", pinataAPISecret)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("upload failed: %s", string(b))
	}

	var result struct {
		IpfsHash string `json:"IpfsHash"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.IpfsHash, nil
}

func distributeCiphertext(ciphertext string, count int, keyID int) {
	_, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		fmt.Printf("Error decoding ciphertext: %v\n", err)
		return
	}

	chunkSize := (len(ciphertext) + count - 1) / count
	var resolver strings.Builder

	for i := 0; i < len(ciphertext); i += chunkSize {
		end := i + chunkSize
		if end > len(ciphertext) {
			end = len(ciphertext)
		}
		chunk := ciphertext[i:end]

		cid, err := uploadToPinata(chunk, pinataAPIKey, pinataAPISecret)
		if err != nil {
			fmt.Printf("Error uploading to Pinata: %v\n", err)
			return
		}
		fmt.Fprintf(&resolver, "%s:%d:%d\n", cid, i, end-1)
	}

	resolverStr := resolver.String()
	fmt.Printf("Resolver: %s\n", resolverStr)

	var fileName string = "resolver_" + fmt.Sprintf("%d", keyID) + ".txt"
	if err := os.WriteFile(fileName, []byte(resolverStr), 0644); err != nil {
		fmt.Printf("Error writing resolver to file: %v\n", err)
	}
}

func generateSharedSecret(keyID int) {
	// Fetch public key from vault
	client, err := api.NewClient(&api.Config{Address: vaultAddr})
	if err != nil {
		fmt.Printf("Error connecting to Vault: %v\n", err)
		return
	}
	client.SetToken(vaultToken)

	secret, err := client.Logical().Read(fmt.Sprintf("secret/data/key/%d", keyID))
	if err != nil || secret == nil {
		fmt.Printf("Error fetching key from Vault: %v\n", err)
		return
	}
	pubKeyB64 := secret.Data["data"].(map[string]interface{})["public_key"].(string)
	pubKeyBytes, err := base64.StdEncoding.DecodeString(pubKeyB64)
	if err != nil {
		fmt.Printf("Error decoding public key: %v\n", err)
		return
	}

	// Load Kyber public key
	scheme := kyber512.Scheme()
	publicKey, err := scheme.UnmarshalBinaryPublicKey(pubKeyBytes)
	if err != nil {
		fmt.Printf("Error unmarshaling public key: %v\n", err)
		return
	}

	cipherText, sharedSecret, err := scheme.Encapsulate(publicKey)
	if err != nil {
		fmt.Printf("Error encapsulating with Kyber: %v\n", err)
		return
	}
	cipherTextB64 := base64.StdEncoding.EncodeToString(cipherText)
	sharedSecretB64 := base64.StdEncoding.EncodeToString(sharedSecret)
	fmt.Println("Shared Secret:", sharedSecretB64)
	fmt.Println("Do not share the shared secret with anyone!")

	supabaseClient, err := supabase.NewClient(supabaseURL, supabaseKey, nil)
	if err != nil {
		fmt.Printf("Error connecting to Supabase: %v\n", err)
		return
	}

	data := map[string]interface{}{
		"cipherText": cipherTextB64,
		// "sharedSecret": sharedSecretB64,
		"created_at": time.Now().UTC().Format(time.RFC3339),
	}
	var result []map[string]interface{}
	_, err = supabaseClient.From("resolvers").Insert(data, false, "", "", "").ExecuteTo(&result)
	if err != nil {
		fmt.Printf("Error inserting into Supabase: %v\n", err)
		return
	}
	fmt.Println("Data deployed to Supabase")
}

// 5. Decrypt from Supabase
func decryptFromSupabase(rowID, keyID int) {
	supabaseClient, err := supabase.NewClient(supabaseURL, supabaseKey, nil)
	if err != nil {
		fmt.Printf("Error connecting to Supabase: %v\n", err)
		return
	}

	var results []map[string]interface{}
	_, err = supabaseClient.From("resolvers").Select("*", "exact", false).Eq("id", fmt.Sprintf("%d", rowID)).ExecuteTo(&results)
	if err != nil || len(results) == 0 {
		fmt.Printf("Error fetching from Supabase: %v\n", err)
		return
	}

	var fileName string = "resolver_" + fmt.Sprintf("%d", rowID) + ".txt"
	resolverBytes, err := os.ReadFile(fileName)
	if err != nil {
		fmt.Printf("Error reading resolver file: %v\n", err)
		return
	}
	resolverData := string(resolverBytes)

	cipherTextB64 := results[0]["cipherText"].(string)
	cipherText, err := base64.StdEncoding.DecodeString(cipherTextB64)
	if err != nil {
		fmt.Printf("Error decoding cipherText: %v\n", err)
		return
	}

	// Fetch private key from vault
	client, err := api.NewClient(&api.Config{Address: vaultAddr})
	if err != nil {
		fmt.Printf("Error connecting to Vault: %v\n", err)
		return
	}
	client.SetToken(vaultToken)

	secret, err := client.Logical().Read(fmt.Sprintf("secret/data/key/%d", keyID))
	if err != nil || secret == nil {
		fmt.Printf("Error fetching key from Vault: %v\n", err)
		return
	}
	privKeyB64 := secret.Data["data"].(map[string]interface{})["private_key"].(string)
	privKeyBytes, err := base64.StdEncoding.DecodeString(privKeyB64)
	if err != nil {
		fmt.Printf("Error decoding private key: %v\n", err)
		return
	}

	// Load Kyber private key
	scheme := kyber512.Scheme()
	privateKey, err := scheme.UnmarshalBinaryPrivateKey(privKeyBytes)
	if err != nil {
		fmt.Printf("Error unmarshaling private key: %v\n", err)
		return
	}

	sharedSecret, err := scheme.Decapsulate(privateKey, cipherText)
	if err != nil {
		fmt.Printf("Error decapsulating Kyber: %v\n", err)
		return
	}
	fmt.Printf("sharedSecret has been obtained: %s\n", base64.StdEncoding.EncodeToString(sharedSecret))

	// Fetch ciphertext chunks from IPFS
	var ciphertext bytes.Buffer
	for _, line := range strings.Split(resolverData, "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, ":")
		hash := parts[0]

		resp, err := http.Get("https://gateway.pinata.cloud/ipfs/" + hash)
		if err != nil {
			fmt.Printf("Error fetching from IPFS gateway: %v\n", err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			fmt.Printf("Error: IPFS gateway returned status %d\n", resp.StatusCode)
			return
		}
		chunkBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			fmt.Printf("Error reading IPFS chunk: %v\n", err)
			return
		}
		ciphertext.Write(chunkBytes)
	}

	sharedSecretStr := base64.StdEncoding.EncodeToString(sharedSecret)
	identity, err := age.NewScryptIdentity(sharedSecretStr)
	if err != nil {
		fmt.Printf("Error parsing scrypt identity: %v\n", err)
		return
	}

	decodedBytes, err := base64.StdEncoding.DecodeString(ciphertext.String())
	if err != nil {
		fmt.Printf("Error decoding base64: %v\n", err)
		return
	}

	r, err := age.Decrypt(bytes.NewReader(decodedBytes), identity)
	if err != nil {
		fmt.Printf("Error decrypting: %v\n", err)
		return
	}
	plaintext, err := io.ReadAll(r)
	if err != nil {
		fmt.Printf("Error reading decrypted plaintext: %v\n", err)
		return
	}
	fmt.Printf("\n\nDecrypted plaintext: %s\n", string(plaintext))
}
