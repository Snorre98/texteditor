// The Vue app's API boundary: the generated Hey API SDK (src/generated/sdk.gen.ts,
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
// Mirrors client/tui/src/api/client.ts (ADR-0037 §4), plus `saveDocument`
// (ADR-0038) and `listDirectory` (ADR-0035) which the editor needs.
import type { ZodType } from "zod";
import type { BlockWrite } from "../generated/types.gen";
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
  getSessionMessages,
  listDirectory,
  listModels,
  listModes,
  listSessions,
  listTools,
  openDocument,
  saveDocument,
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
  zGetSessionMessagesResponse,
  zListDirectoryResponse,
  zListModelsResponse,
  zListModesResponse,
  zListSessionsResponse,
  zListToolsResponse,
  zOpenDocumentResponse,
  zSaveDocumentResponse,
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
  listModes: () =>
    listModes({ client, responseValidator: validator(zListModesResponse) }),
  listTools: () =>
    listTools({ client, responseValidator: validator(zListToolsResponse) }),
  listDirectory: (path: string) =>
    listDirectory({
      client,
      query: { path },
      responseValidator: validator(zListDirectoryResponse),
    }),
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
  saveDocument: (id: string, body: { blocks: BlockWrite[] }) =>
    saveDocument({
      client,
      path: { id },
      body,
      responseValidator: validator(zSaveDocumentResponse),
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
