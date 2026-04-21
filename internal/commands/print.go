package commands

import (
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
)

func isFish() bool {
	return strings.Contains(os.Getenv("SHELL"), "fish")
}

func printRequest(req *http.Request, body []byte, format, secretKey string) error {
	switch format {
	case "curl":
		return printCurl(req, body, secretKey)
	case "python":
		return printPython(req, body, secretKey)
	default:
		return fmt.Errorf("unknown --print format %q (supported: curl, python)", format)
	}
}

func printCurl(req *http.Request, body []byte, secretKey string) error {
	if isFish() {
		return printCurlFish(req, body, secretKey)
	}
	return printCurlBash(req, body, secretKey)
}

func printCurlBash(req *http.Request, body []byte, secretKey string) error {
	accessKey := req.Header.Get("X-Cmp-AccessKey")
	projectID := req.Header.Get("X-Cmp-ProjectId")
	rawURL := req.URL.String()

	var sb strings.Builder
	sb.WriteString("(\n")
	sb.WriteString(fmt.Sprintf("  URL=%q\n", rawURL))
	sb.WriteString(fmt.Sprintf("  ACCESS_KEY=%q\n", accessKey))
	sb.WriteString(fmt.Sprintf("  PROJECT_ID=%q\n", projectID))
	sb.WriteString(fmt.Sprintf("  SECRET_KEY=%q\n", secretKey))
	sb.WriteString("  TS=$(date +%s%3N)\n")
	sb.WriteString(fmt.Sprintf(`  SIG=$(echo -n "%s${URL}${TS}${ACCESS_KEY}${PROJECT_ID}OpenApi" | openssl dgst -sha256 -hmac "$SECRET_KEY" -binary | base64)`+"\n\n", req.Method))

	sb.WriteString(fmt.Sprintf("  curl -X %s \"$URL\"", req.Method))
	writeCurlHeaders(&sb, req, body)
	sb.WriteString(")\n")
	fmt.Print(sb.String())
	return nil
}

func printCurlFish(req *http.Request, body []byte, secretKey string) error {
	accessKey := req.Header.Get("X-Cmp-AccessKey")
	projectID := req.Header.Get("X-Cmp-ProjectId")
	rawURL := req.URL.String()

	var sb strings.Builder
	sb.WriteString("begin\n")
	sb.WriteString(fmt.Sprintf("  set -l URL %q\n", rawURL))
	sb.WriteString(fmt.Sprintf("  set -l ACCESS_KEY %q\n", accessKey))
	sb.WriteString(fmt.Sprintf("  set -l PROJECT_ID %q\n", projectID))
	sb.WriteString(fmt.Sprintf("  set -l SECRET_KEY %q\n", secretKey))
	sb.WriteString("  set -l TS (date +%s%3N)\n")
	sb.WriteString(fmt.Sprintf("  set -l MSG (string join '' %s $URL $TS $ACCESS_KEY $PROJECT_ID OpenApi)\n", req.Method))
	sb.WriteString("  set -l SIG (echo -n $MSG | openssl dgst -sha256 -hmac $SECRET_KEY -binary | base64)\n\n")

	sb.WriteString(fmt.Sprintf("  curl -X %s $URL", req.Method))
	writeCurlHeaders(&sb, req, body)
	sb.WriteString("end\n")
	fmt.Print(sb.String())
	return nil
}

func writeCurlHeaders(sb *strings.Builder, req *http.Request, body []byte) {
	for _, h := range sortedHeaders(req) {
		switch h {
		case "X-Cmp-AccessKey":
			sb.WriteString(" \\\n    -H \"X-Cmp-AccessKey: $ACCESS_KEY\"")
		case "X-Cmp-ProjectId":
			sb.WriteString(" \\\n    -H \"X-Cmp-ProjectId: $PROJECT_ID\"")
		case "X-Cmp-Signature":
			sb.WriteString(" \\\n    -H \"X-Cmp-Signature: $SIG\"")
		case "X-Cmp-Timestamp":
			sb.WriteString(" \\\n    -H \"X-Cmp-Timestamp: $TS\"")
		default:
			fmt.Fprintf(sb, " \\\n    -H '%s: %s'", h, req.Header.Get(h))
		}
	}
	if len(body) > 0 {
		fmt.Fprintf(sb, " \\\n    -d '%s'", string(body))
	}
	sb.WriteString("\n")
}

func printPython(req *http.Request, body []byte, secretKey string) error {
	accessKey := req.Header.Get("X-Cmp-AccessKey")
	projectID := req.Header.Get("X-Cmp-ProjectId")
	rawURL := req.URL.String()

	var sb strings.Builder
	sb.WriteString("import base64, hashlib, hmac, json, time, urllib.request\n\n")
	sb.WriteString(fmt.Sprintf("url = %q\n", rawURL))
	sb.WriteString(fmt.Sprintf("access_key = %q\n", accessKey))
	sb.WriteString(fmt.Sprintf("project_id = %q\n", projectID))
	sb.WriteString(fmt.Sprintf("secret_key = %q\n\n", secretKey))
	sb.WriteString("ts = str(int(time.time() * 1000))\n")
	sb.WriteString(fmt.Sprintf("msg = %q + url + ts + access_key + project_id + \"OpenApi\"\n", req.Method))
	sb.WriteString("sig = base64.b64encode(hmac.new(secret_key.encode(), msg.encode(), hashlib.sha256).digest()).decode()\n\n")

	sb.WriteString("req = urllib.request.Request(url, headers={\n")
	for _, h := range sortedHeaders(req) {
		switch h {
		case "X-Cmp-AccessKey":
			sb.WriteString("    \"X-Cmp-AccessKey\": access_key,\n")
		case "X-Cmp-ProjectId":
			sb.WriteString("    \"X-Cmp-ProjectId\": project_id,\n")
		case "X-Cmp-Signature":
			sb.WriteString("    \"X-Cmp-Signature\": sig,\n")
		case "X-Cmp-Timestamp":
			sb.WriteString("    \"X-Cmp-Timestamp\": ts,\n")
		default:
			sb.WriteString(fmt.Sprintf("    %q: %q,\n", h, req.Header.Get(h)))
		}
	}
	if len(body) > 0 {
		sb.WriteString(fmt.Sprintf("}, data=%q)\n", string(body)))
	} else {
		sb.WriteString("})\n")
	}
	sb.WriteString("\nwith urllib.request.urlopen(req) as resp:\n")
	sb.WriteString("    print(json.loads(resp.read()))\n")

	fmt.Print(sb.String())
	return nil
}

func sortedHeaders(req *http.Request) []string {
	canonical := map[string]string{
		"X-Cmp-Accesskey":  "X-Cmp-AccessKey",
		"X-Cmp-Signature":  "X-Cmp-Signature",
		"X-Cmp-Timestamp":  "X-Cmp-Timestamp",
		"X-Cmp-Clienttype": "X-Cmp-ClientType",
		"X-Cmp-Projectid":  "X-Cmp-ProjectId",
		"X-Cmp-Language":   "X-Cmp-Language",
		"Content-Type":     "Content-Type",
	}
	headers := make([]string, 0, len(req.Header))
	for h := range req.Header {
		if c, ok := canonical[h]; ok {
			headers = append(headers, c)
		} else {
			headers = append(headers, h)
		}
	}
	sort.Strings(headers)
	return headers
}
