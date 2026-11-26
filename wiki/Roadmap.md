# Roadmap

Not built yet, roughly in order:

- **Iterate** over large results in pages (keyset by id)
- **Change feed**: read changes since a position, for projections and events
- **More operators**: case-insensitive match, contains on JSON arrays
- **Projections**: read only some columns
- **Bulk writes**: insert and upsert many rows in one statement
- **Hooks** before and after writes
- **More stores**, e.g. MySQL

Not planned: **joins**. Apps load related rows in batches instead: the list
first, then every row it refers to in one `In` (or `GetMany`), which works for
any number of ids. This keeps every store simple and the same, also in
memory.
