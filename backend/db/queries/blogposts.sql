-- The digest re-reads the same feed daily, so this is what stops it filing a
-- fresh ticket for the same post on every run.
-- name: KnownBlogURLs :many
select post_url
  from blog_posts
 where org_id = sqlc.arg(org_id)
   and account_id = sqlc.arg(account_id)
   and post_url = any(sqlc.arg(post_urls)::text[]);

-- The unique index on (org_id, account_id, post_url) is the real guarantee: a
-- replayed or concurrent workflow is rejected by the database rather than by
-- application ordering.
-- name: RecordBlogPost :exec
insert into blog_posts (org_id, account_id, post_url, title, published_at, summary, ticket_id)
values (
    sqlc.arg(org_id), sqlc.arg(account_id), sqlc.arg(post_url),
    left(sqlc.arg(title), 500), sqlc.narg(published_at),
    left(sqlc.arg(summary), 4000), sqlc.narg(ticket_id)
);
