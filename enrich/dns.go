package enrich

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/miekg/dns"
)

type dnsRecords struct{ A, AAAA, CNAME []string }

// 通过同一任务 HTTP 代理发送 DNS wire-format 查询，不调用本机 UDP 解析器。
func queryDNS(ctx context.Context, client *http.Client, endpoint, host string) (*dnsRecords, error) {
	result := &dnsRecords{}
	for _, typ := range []uint16{dns.TypeA, dns.TypeAAAA, dns.TypeCNAME} {
		question := new(dns.Msg)
		question.SetQuestion(dns.Fqdn(host), typ)
		payload, err := question.Pack()
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/dns-message")
		req.Header.Set("Accept", "application/dns-message")
		response, err := client.Do(req)
		if err != nil {
			return nil, errors.New("任务 DNS 出站失败")
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 65536))
		response.Body.Close()
		contentType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
		if err != nil || len(body) > 65535 || response.StatusCode != 200 || contentType != "application/dns-message" {
			return nil, errors.New("任务 DNS 响应无效")
		}
		answer := new(dns.Msg)
		if answer.Unpack(body) != nil || !answer.Response || answer.Truncated || answer.Id != question.Id || len(answer.Question) != 1 || answer.Question[0] != question.Question[0] || (answer.Rcode != dns.RcodeSuccess && answer.Rcode != dns.RcodeNameError) {
			return nil, errors.New("任务 DNS 响应不匹配")
		}
		for _, record := range answer.Answer {
			switch value := record.(type) {
			case *dns.A:
				result.A = append(result.A, value.A.String())
			case *dns.AAAA:
				result.AAAA = append(result.AAAA, value.AAAA.String())
			case *dns.CNAME:
				result.CNAME = append(result.CNAME, strings.TrimSuffix(value.Target, "."))
			}
		}
	}
	return result, nil
}
