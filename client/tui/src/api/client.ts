// The TUI's API boundary: the generated Hey API SDK (src/generated/sdk.gen.ts,
// regenerated from api/openapi.yaml — never hand-shaped) with two additions the
// ADRs mandate for the client:
//
//   1. Zod runtime validation of every response (ADR-0017 §2): each generated
//      call is bound to its generated zod schema via the client-fetch
//      `responseValidator` hook. The LAN target (ADR-0014) is not trusted
//      localhost, so boundary validation is defense-in-depth, not redundant.
//   2. Base-URL injection: `setEndpoint` rewrites the client base URL from the
//      discovery module (ADR-0021 §1), after which every call below targets the
//      resolved engine.
//
// `/turn` streaming is NOT one of the generated calls: the route is
// x-ogen-raw-response (ADR-0031), and the stream reader lives in ./sse.ts.
import type { ZodType } from "zod";
import { client as sdkClient } from "../generated/sdk.gen";
import {
  applyEdit,
  commitDocument,
  createSession,
  getBlocks,
  getCandidates,
  getDiff,
  getHealth,
  getHistory,
  getModelStatus,
  getSessionMessages,
  listModels,
  listModes,
  listSessions,
  listTools,
  openDocument,
  provisionModel,
  startModel,
  stopModel,
} from "../generated/sdk.gen";
import {
  zApplyEditResponse,
  zCommitDocumentResponse,
  zCreateSessionResponse,
  zGetBlocksResponse,
  zGetCandidatesResponse,
  zGetDiffResponse,
  zGetHealthResponse,
  zGetHistoryResponse,
  zGetModelStatusResponse,
  zGetSessionMessagesResponse,
  zListModelsResponse,
  zListModesResponse,
  zListSessionsResponse,
  zListToolsResponse,
  zOpenDocumentResponse,
  zProvisionModelResponse,
  zStartModelResponse,
  zStopModelResponse,
} from "../generated/zod.gen";

export { sdkClient };
export type * from "../generated/types.gen";

// The generated default client, shared by every call below. Discovery rewrites
// its baseUrl once before the store issues any request.
export const client = sdkClient;

export function setEndpoint(baseUrl: string): void {
  client.setConfig({ baseUrl });
}

// validator binds a generated zod schema to client-fetch's responseValidator
// hook: a non-conforming response body is a hard, labeled failure at the
// boundary (ADR-0017 §2).
function validator<T>(schema: ZodType<T>) {
  return (data: unknown) => schema.parseAsync(data) as Promise<unknown>;
}

export const api = {
  health: () =>
    getHealth({ client, responseValidator: validator(zGetHealthResponse) }),
  listModels: () =>
    listModels({ client, responseValidator: validator(zListModelsResponse) }),
  startModel: (name: string) =>
    startModel({
      client,
      path: { name },
      responseValidator: validator(zStartModelResponse),
    }),
  stopModel: (name: string) =>
    stopModel({
      client,
      path: { name },
      responseValidator: validator(zStopModelResponse),
    }),
  provisionModel: (name: string) =>
    provisionModel({
      client,
      path: { name },
      responseValidator: validator(zProvisionModelResponse),
    }),
  getModelStatus: (name: string) =>
    getModelStatus({
      client,
      path: { name },
      responseValidator: validator(zGetModelStatusResponse),
    }),
  listModes: () =>
    listModes({ client, responseValidator: validator(zListModesResponse) }),
  listTools: () =>
    listTools({ client, responseValidator: validator(zListToolsResponse) }),
  openDocument: (path: string) =>
    openDocument({
      client,
      body: { path },
      responseValidator: validator(zOpenDocumentResponse),
    }),
  getBlocks: (id: string) =>
    getBlocks({
      client,
      path: { id },
      responseValidator: validator(zGetBlocksResponse),
    }),
  applyEdit: (id: string, body: { blockId: string; text: string }) =>
    applyEdit({
      client,
      path: { id },
      body,
      responseValidator: validator(zApplyEditResponse),
    }),
  commitDocument: (id: string) =>
    commitDocument({
      client,
      path: { id },
      responseValidator: validator(zCommitDocumentResponse),
    }),
  getHistory: (id: string) =>
    getHistory({
      client,
      path: { id },
      responseValidator: validator(zGetHistoryResponse),
    }),
  getDiff: (id: string, base: string, rev: string) =>
    getDiff({
      client,
      path: { id },
      query: { base, rev },
      responseValidator: validator(zGetDiffResponse),
    }),
  getCandidates: (id: string, bid: string) =>
    getCandidates({
      client,
      path: { id, bid },
      responseValidator: validator(zGetCandidatesResponse),
    }),
  listSessions: (documentId: string) =>
    listSessions({
      client,
      query: { documentId },
      responseValidator: validator(zListSessionsResponse),
    }),
  createSession: (body: {
    documentId: string;
    anchorBlockId?: string;
    modeType?: string;
  }) =>
    createSession({
      client,
      body,
      responseValidator: validator(zCreateSessionResponse),
    }),
  getSessionMessages: (id: string) =>
    getSessionMessages({
      client,
      path: { id },
      responseValidator: validator(zGetSessionMessagesResponse),
    }),
};

export type Api = typeof api;
