// 浏览器端 WebAuthn / Passkey 编码与交互辅助（支持真实浏览器安全密钥与演示模式模拟）。
import { MOCK } from "../api/client";

function fromBase64Url(str: string): ArrayBuffer {
  const b64 = str.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((str.length + 3) % 4);
  const bin = atob(b64);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return bytes.buffer;
}

function toBase64Url(buf: ArrayBuffer | null | undefined): string {
  if (!buf) return "";
  const bytes = new Uint8Array(buf);
  let bin = "";
  for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
  return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/g, "");
}

export function passkeySupported(): boolean {
  if (MOCK) return true;
  return typeof window !== "undefined" && typeof window.PublicKeyCredential !== "undefined" && !!navigator.credentials;
}

export async function createPasskeyCredential(optionsWrapper: Record<string, unknown>): Promise<Record<string, unknown>> {
  if (MOCK) {
    return { id: "mock_cred_" + Math.random().toString(36).slice(2, 10), type: "public-key" };
  }
  const pk = ((optionsWrapper.publicKey ?? optionsWrapper) as Record<string, unknown>);
  const parseCreation = (PublicKeyCredential as unknown as { parseCreationOptionsFromJSON?: (o: unknown) => PublicKeyCredentialCreationOptions }).parseCreationOptionsFromJSON;
  const publicKey: PublicKeyCredentialCreationOptions = parseCreation
    ? parseCreation(pk)
    : {
        ...(pk as unknown as PublicKeyCredentialCreationOptions),
        challenge: fromBase64Url(String(pk.challenge)),
        user: {
          ...((pk.user ?? {}) as PublicKeyCredentialUserEntity),
          id: fromBase64Url(String((pk.user as { id: string }).id)),
        },
        excludeCredentials: Array.isArray(pk.excludeCredentials)
          ? pk.excludeCredentials.map((c: { id: string; type: PublicKeyCredentialType; transports?: AuthenticatorTransport[] }) => ({
              ...c,
              id: fromBase64Url(c.id),
            }))
          : undefined,
      };
  const cred = (await navigator.credentials.create({ publicKey })) as PublicKeyCredential | null;
  if (!cred) throw new Error("invalid_passkey");
  const toJson = (cred as unknown as { toJSON?: () => Record<string, unknown> }).toJSON;
  if (toJson) return toJson.call(cred);
  const res = cred.response as AuthenticatorAttestationResponse;
  return {
    id: cred.id,
    rawId: toBase64Url(cred.rawId),
    type: cred.type,
    authenticatorAttachment: cred.authenticatorAttachment ?? undefined,
    clientExtensionResults: cred.getClientExtensionResults?.() ?? {},
    response: {
      attestationObject: toBase64Url(res.attestationObject),
      clientDataJSON: toBase64Url(res.clientDataJSON),
      transports: res.getTransports?.() ?? [],
    },
  };
}

export async function getPasskeyAssertion(optionsWrapper: Record<string, unknown>): Promise<Record<string, unknown>> {
  if (MOCK) {
    return { id: "mock_cred", type: "public-key" };
  }
  const pk = ((optionsWrapper.publicKey ?? optionsWrapper) as Record<string, unknown>);
  const parseRequest = (PublicKeyCredential as unknown as { parseRequestOptionsFromJSON?: (o: unknown) => PublicKeyCredentialRequestOptions }).parseRequestOptionsFromJSON;
  const publicKey: PublicKeyCredentialRequestOptions = parseRequest
    ? parseRequest(pk)
    : {
        ...(pk as unknown as PublicKeyCredentialRequestOptions),
        challenge: fromBase64Url(String(pk.challenge)),
        allowCredentials: Array.isArray(pk.allowCredentials)
          ? pk.allowCredentials.map((c: { id: string; type: PublicKeyCredentialType; transports?: AuthenticatorTransport[] }) => ({
              ...c,
              id: fromBase64Url(c.id),
            }))
          : undefined,
      };
  const cred = (await navigator.credentials.get({ publicKey })) as PublicKeyCredential | null;
  if (!cred) throw new Error("invalid_passkey");
  const toJson = (cred as unknown as { toJSON?: () => Record<string, unknown> }).toJSON;
  if (toJson) return toJson.call(cred);
  const res = cred.response as AuthenticatorAssertionResponse;
  return {
    id: cred.id,
    rawId: toBase64Url(cred.rawId),
    type: cred.type,
    authenticatorAttachment: cred.authenticatorAttachment ?? undefined,
    clientExtensionResults: cred.getClientExtensionResults?.() ?? {},
    response: {
      authenticatorData: toBase64Url(res.authenticatorData),
      clientDataJSON: toBase64Url(res.clientDataJSON),
      signature: toBase64Url(res.signature),
      userHandle: res.userHandle ? toBase64Url(res.userHandle) : null,
    },
  };
}
