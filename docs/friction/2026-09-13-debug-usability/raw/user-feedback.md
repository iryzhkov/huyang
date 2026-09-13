# User feedback from another run

Received during this workshop, 2026-09-13. Original feedback follows unchanged.

The strongest friction was response volume:

•	Reads sometimes exposed the same content in both text and structured output, making tool results unnecessarily large.
•	Line limits aren’t enough. UpKeeper’s manifest contains large encoded payloads; I had to parse results inside the orchestration tool and print selected fields to keep them manageable.
•	Mutation responses also benefited from manually printing only the outcome and summary. Compact results should be easy to request directly.

My priorities would be byte-bounded reads, compact response modes, and clearer separation between concise receipts and optional diagnostics. The existing oversized workspace-status response belongs in that same cleanup.
