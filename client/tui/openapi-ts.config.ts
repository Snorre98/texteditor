import { defineConfig } from "@hey-api/openapi-ts";
export default defineConfig({
  input: "../../api/openapi.yaml",
  output: { path: "src/generated" },
  client: "@hey-api/client-fetch",
  plugins: ["zod", "@hey-api/sdk"],
});
