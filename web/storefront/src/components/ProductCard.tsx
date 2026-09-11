import { mediaUrl } from "../api";
import { formatMoney } from "../types";
import type { Money } from "../types";

// Structural rather than `Hit`-specific so callers with a plain `Product`
// (e.g. a shop's storefront listing) can pass one in without remapping.
export interface CardItem {
  title: string;
  list_price: Money;
  primary_media_key: string;
}

export function ProductCard({ hit }: Readonly<{ hit: CardItem }>) {
  return (
    <article className="card">
      <div className="card-img">
        {hit.primary_media_key ? (
          <img src={mediaUrl(hit.primary_media_key)} alt={hit.title} loading="lazy" />
        ) : (
          <div className="noimg" aria-hidden />
        )}
      </div>
      <h2>{hit.title}</h2>
      <p className="price">{formatMoney(hit.list_price)}</p>
    </article>
  );
}
