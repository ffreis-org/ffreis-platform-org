package cmd

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
)

const (
	testIAMActionKey = "Action"
	testIAMRoleName  = "my-role"
)

func testIAMClient(t *testing.T, handler http.HandlerFunc) *iam.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return iam.New(iam.Options{
		Region:       testRegion,
		Credentials:  credentials.NewStaticCredentialsProvider("AKIA", "secret", "token"),
		BaseEndpoint: sdkaws.String(server.URL),
		HTTPClient:   server.Client(),
	})
}

func iamXMLError(code, message string) string {
	return fmt.Sprintf(`<ErrorResponse>
  <Error>
    <Type>Sender</Type>
    <Code>%s</Code>
    <Message>%s</Message>
  </Error>
  <RequestId>abc123</RequestId>
</ErrorResponse>`, code, message)
}

// --- deleteAllInlineRolePolicies ---

func TestDeleteAllInlineRolePoliciesNotFoundOnList(t *testing.T) {
	t.Parallel()
	client := testIAMClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeTextXML)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintln(w, iamXMLError("NoSuchEntity", "The role cannot be found."))
	})
	if err := deleteAllInlineRolePolicies(context.Background(), client, "missing-role"); err != nil {
		t.Fatalf("expected nil for not-found list error, got: %v", err)
	}
}

func TestDeleteAllInlineRolePoliciesOtherListError(t *testing.T) {
	t.Parallel()
	client := testIAMClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeTextXML)
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprintln(w, iamXMLError("AccessDenied", "Access denied"))
	})
	if err := deleteAllInlineRolePolicies(context.Background(), client, testIAMRoleName); err == nil {
		t.Fatal("expected error for access denied on list")
	}
}

func TestDeleteAllInlineRolePoliciesEmptyList(t *testing.T) {
	t.Parallel()
	client := testIAMClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeTextXML)
		_ = r.ParseForm()
		if r.FormValue(testIAMActionKey) != "ListRolePolicies" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprint(w, `<ListRolePoliciesResponse>
  <ListRolePoliciesResult>
    <IsTruncated>false</IsTruncated>
    <PolicyNames/>
  </ListRolePoliciesResult>
  <ResponseMetadata><RequestId>abc</RequestId></ResponseMetadata>
</ListRolePoliciesResponse>`)
	})
	if err := deleteAllInlineRolePolicies(context.Background(), client, testIAMRoleName); err != nil {
		t.Fatalf("expected nil for empty policy list, got: %v", err)
	}
}

func TestDeleteAllInlineRolePoliciesDeletesOnePolicy(t *testing.T) {
	t.Parallel()
	deleteCalls := 0
	client := testIAMClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeTextXML)
		_ = r.ParseForm()
		switch r.FormValue(testIAMActionKey) {
		case "ListRolePolicies":
			_, _ = fmt.Fprint(w, `<ListRolePoliciesResponse>
  <ListRolePoliciesResult>
    <IsTruncated>false</IsTruncated>
    <PolicyNames><member>inline-policy</member></PolicyNames>
  </ListRolePoliciesResult>
  <ResponseMetadata><RequestId>abc</RequestId></ResponseMetadata>
</ListRolePoliciesResponse>`)
		case "DeleteRolePolicy":
			deleteCalls++
			_, _ = fmt.Fprint(w, `<DeleteRolePolicyResponse>
  <ResponseMetadata><RequestId>abc</RequestId></ResponseMetadata>
</DeleteRolePolicyResponse>`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	if err := deleteAllInlineRolePolicies(context.Background(), client, testIAMRoleName); err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
	if deleteCalls != 1 {
		t.Fatalf("expected 1 delete call, got %d", deleteCalls)
	}
}

func TestDeleteAllInlineRolePoliciesTruncatedList(t *testing.T) {
	t.Parallel()
	listCalls, deleteCalls := 0, 0
	client := testIAMClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeTextXML)
		_ = r.ParseForm()
		switch r.FormValue(testIAMActionKey) {
		case "ListRolePolicies":
			listCalls++
			if listCalls == 1 {
				_, _ = fmt.Fprint(w, `<ListRolePoliciesResponse>
  <ListRolePoliciesResult>
    <IsTruncated>true</IsTruncated>
    <Marker>page-marker</Marker>
    <PolicyNames><member>policy-page1</member></PolicyNames>
  </ListRolePoliciesResult>
  <ResponseMetadata><RequestId>abc</RequestId></ResponseMetadata>
</ListRolePoliciesResponse>`)
			} else {
				_, _ = fmt.Fprint(w, `<ListRolePoliciesResponse>
  <ListRolePoliciesResult>
    <IsTruncated>false</IsTruncated>
    <PolicyNames><member>policy-page2</member></PolicyNames>
  </ListRolePoliciesResult>
  <ResponseMetadata><RequestId>abc</RequestId></ResponseMetadata>
</ListRolePoliciesResponse>`)
			}
		case "DeleteRolePolicy":
			deleteCalls++
			_, _ = fmt.Fprint(w, `<DeleteRolePolicyResponse>
  <ResponseMetadata><RequestId>abc</RequestId></ResponseMetadata>
</DeleteRolePolicyResponse>`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	if err := deleteAllInlineRolePolicies(context.Background(), client, testIAMRoleName); err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
	if listCalls != 2 {
		t.Fatalf("expected 2 list calls, got %d", listCalls)
	}
	if deleteCalls != 2 {
		t.Fatalf("expected 2 delete calls, got %d", deleteCalls)
	}
}

func TestDeleteAllInlineRolePoliciesDeleteNotFound(t *testing.T) {
	t.Parallel()
	client := testIAMClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeTextXML)
		_ = r.ParseForm()
		switch r.FormValue(testIAMActionKey) {
		case "ListRolePolicies":
			_, _ = fmt.Fprint(w, `<ListRolePoliciesResponse>
  <ListRolePoliciesResult>
    <IsTruncated>false</IsTruncated>
    <PolicyNames><member>gone-policy</member></PolicyNames>
  </ListRolePoliciesResult>
  <ResponseMetadata><RequestId>abc</RequestId></ResponseMetadata>
</ListRolePoliciesResponse>`)
		case "DeleteRolePolicy":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintln(w, iamXMLError("NoSuchEntity", "The policy cannot be found."))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	if err := deleteAllInlineRolePolicies(context.Background(), client, testIAMRoleName); err != nil {
		t.Fatalf("delete not-found should be treated as ok, got: %v", err)
	}
}

func TestDeleteAllInlineRolePoliciesDeleteError(t *testing.T) {
	t.Parallel()
	client := testIAMClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeTextXML)
		_ = r.ParseForm()
		switch r.FormValue(testIAMActionKey) {
		case "ListRolePolicies":
			_, _ = fmt.Fprint(w, `<ListRolePoliciesResponse>
  <ListRolePoliciesResult>
    <IsTruncated>false</IsTruncated>
    <PolicyNames><member>inline-policy</member></PolicyNames>
  </ListRolePoliciesResult>
  <ResponseMetadata><RequestId>abc</RequestId></ResponseMetadata>
</ListRolePoliciesResponse>`)
		case "DeleteRolePolicy":
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprintln(w, iamXMLError("AccessDenied", "Access denied"))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	if err := deleteAllInlineRolePolicies(context.Background(), client, testIAMRoleName); err == nil {
		t.Fatal("expected error from delete failure")
	}
}

// --- detachAllManagedRolePolicies ---

func TestDetachAllManagedRolePoliciesNotFoundOnList(t *testing.T) {
	t.Parallel()
	client := testIAMClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeTextXML)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintln(w, iamXMLError("NoSuchEntity", "The role cannot be found."))
	})
	if err := detachAllManagedRolePolicies(context.Background(), client, "missing-role"); err != nil {
		t.Fatalf("expected nil for not-found list error, got: %v", err)
	}
}

func TestDetachAllManagedRolePoliciesOtherListError(t *testing.T) {
	t.Parallel()
	client := testIAMClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeTextXML)
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprintln(w, iamXMLError("AccessDenied", "Access denied"))
	})
	if err := detachAllManagedRolePolicies(context.Background(), client, testIAMRoleName); err == nil {
		t.Fatal("expected error for access denied on list")
	}
}

func TestDetachAllManagedRolePoliciesEmptyList(t *testing.T) {
	t.Parallel()
	client := testIAMClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeTextXML)
		_ = r.ParseForm()
		if r.FormValue(testIAMActionKey) != "ListAttachedRolePolicies" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprint(w, `<ListAttachedRolePoliciesResponse>
  <ListAttachedRolePoliciesResult>
    <IsTruncated>false</IsTruncated>
    <AttachedPolicies/>
  </ListAttachedRolePoliciesResult>
  <ResponseMetadata><RequestId>abc</RequestId></ResponseMetadata>
</ListAttachedRolePoliciesResponse>`)
	})
	if err := detachAllManagedRolePolicies(context.Background(), client, testIAMRoleName); err != nil {
		t.Fatalf("expected nil for empty attached policy list, got: %v", err)
	}
}

func TestDetachAllManagedRolePoliciesDetachesOnePolicy(t *testing.T) {
	t.Parallel()
	detachCalls := 0
	client := testIAMClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeTextXML)
		_ = r.ParseForm()
		switch r.FormValue(testIAMActionKey) {
		case "ListAttachedRolePolicies":
			_, _ = fmt.Fprint(w, `<ListAttachedRolePoliciesResponse>
  <ListAttachedRolePoliciesResult>
    <IsTruncated>false</IsTruncated>
    <AttachedPolicies>
      <member>
        <PolicyName>managed-pol</PolicyName>
        <PolicyArn>arn:aws:iam::123456789012:policy/managed-pol</PolicyArn>
      </member>
    </AttachedPolicies>
  </ListAttachedRolePoliciesResult>
  <ResponseMetadata><RequestId>abc</RequestId></ResponseMetadata>
</ListAttachedRolePoliciesResponse>`)
		case "DetachRolePolicy":
			detachCalls++
			_, _ = fmt.Fprint(w, `<DetachRolePolicyResponse>
  <ResponseMetadata><RequestId>abc</RequestId></ResponseMetadata>
</DetachRolePolicyResponse>`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	if err := detachAllManagedRolePolicies(context.Background(), client, testIAMRoleName); err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
	if detachCalls != 1 {
		t.Fatalf("expected 1 detach call, got %d", detachCalls)
	}
}

func TestDetachAllManagedRolePoliciesTruncatedList(t *testing.T) {
	t.Parallel()
	listCalls, detachCalls := 0, 0
	client := testIAMClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeTextXML)
		_ = r.ParseForm()
		switch r.FormValue(testIAMActionKey) {
		case "ListAttachedRolePolicies":
			listCalls++
			if listCalls == 1 {
				_, _ = fmt.Fprint(w, `<ListAttachedRolePoliciesResponse>
  <ListAttachedRolePoliciesResult>
    <IsTruncated>true</IsTruncated>
    <Marker>next-page</Marker>
    <AttachedPolicies>
      <member>
        <PolicyName>pol1</PolicyName>
        <PolicyArn>arn:aws:iam::123456789012:policy/pol1</PolicyArn>
      </member>
    </AttachedPolicies>
  </ListAttachedRolePoliciesResult>
  <ResponseMetadata><RequestId>abc</RequestId></ResponseMetadata>
</ListAttachedRolePoliciesResponse>`)
			} else {
				_, _ = fmt.Fprint(w, `<ListAttachedRolePoliciesResponse>
  <ListAttachedRolePoliciesResult>
    <IsTruncated>false</IsTruncated>
    <AttachedPolicies>
      <member>
        <PolicyName>pol2</PolicyName>
        <PolicyArn>arn:aws:iam::123456789012:policy/pol2</PolicyArn>
      </member>
    </AttachedPolicies>
  </ListAttachedRolePoliciesResult>
  <ResponseMetadata><RequestId>abc</RequestId></ResponseMetadata>
</ListAttachedRolePoliciesResponse>`)
			}
		case "DetachRolePolicy":
			detachCalls++
			_, _ = fmt.Fprint(w, `<DetachRolePolicyResponse>
  <ResponseMetadata><RequestId>abc</RequestId></ResponseMetadata>
</DetachRolePolicyResponse>`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	if err := detachAllManagedRolePolicies(context.Background(), client, testIAMRoleName); err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
	if listCalls != 2 {
		t.Fatalf("expected 2 list calls, got %d", listCalls)
	}
	if detachCalls != 2 {
		t.Fatalf("expected 2 detach calls, got %d", detachCalls)
	}
}

func TestDetachAllManagedRolePoliciesDetachNotFound(t *testing.T) {
	t.Parallel()
	client := testIAMClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeTextXML)
		_ = r.ParseForm()
		switch r.FormValue(testIAMActionKey) {
		case "ListAttachedRolePolicies":
			_, _ = fmt.Fprint(w, `<ListAttachedRolePoliciesResponse>
  <ListAttachedRolePoliciesResult>
    <IsTruncated>false</IsTruncated>
    <AttachedPolicies>
      <member>
        <PolicyName>gone-pol</PolicyName>
        <PolicyArn>arn:aws:iam::123456789012:policy/gone-pol</PolicyArn>
      </member>
    </AttachedPolicies>
  </ListAttachedRolePoliciesResult>
  <ResponseMetadata><RequestId>abc</RequestId></ResponseMetadata>
</ListAttachedRolePoliciesResponse>`)
		case "DetachRolePolicy":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintln(w, iamXMLError("NoSuchEntity", "The policy cannot be found."))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	if err := detachAllManagedRolePolicies(context.Background(), client, testIAMRoleName); err != nil {
		t.Fatalf("detach not-found should be treated as ok, got: %v", err)
	}
}

func TestDetachAllManagedRolePoliciesDetachError(t *testing.T) {
	t.Parallel()
	client := testIAMClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeTextXML)
		_ = r.ParseForm()
		switch r.FormValue(testIAMActionKey) {
		case "ListAttachedRolePolicies":
			_, _ = fmt.Fprint(w, `<ListAttachedRolePoliciesResponse>
  <ListAttachedRolePoliciesResult>
    <IsTruncated>false</IsTruncated>
    <AttachedPolicies>
      <member>
        <PolicyName>managed-pol</PolicyName>
        <PolicyArn>arn:aws:iam::123456789012:policy/managed-pol</PolicyArn>
      </member>
    </AttachedPolicies>
  </ListAttachedRolePoliciesResult>
  <ResponseMetadata><RequestId>abc</RequestId></ResponseMetadata>
</ListAttachedRolePoliciesResponse>`)
		case "DetachRolePolicy":
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprintln(w, iamXMLError("AccessDenied", "Access denied"))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	if err := detachAllManagedRolePolicies(context.Background(), client, testIAMRoleName); err == nil {
		t.Fatal("expected error from detach failure")
	}
}
