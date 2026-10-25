// The Go package embeds ../internal/web/dist; this file keeps the folder in Git.
import { writeFileSync } from "node:fs";
writeFileSync(new URL("../../internal/web/dist/.keep", import.meta.url), "");
