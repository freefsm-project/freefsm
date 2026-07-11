# Invoice Settlement

This context describes how customer value settles invoices while preserving the financial history of every receipt, application, refund, and correction.

## Language

**Customer**:
The person or organization responsible for invoices and the owner of any credit created by overpayment.
_Avoid_: Account, client

**Invoice**:
A request for payment belonging to exactly one Customer.
_Avoid_: Bill, payment

**Invoice Status**:
The workflow state of an Invoice, independent of whether its value has been settled.
_Avoid_: Payment status, monetary status

**Payment**:
An immutable positive receipt against one Invoice.
_Avoid_: Credit, transaction

**Settlement State**:
The monetary state of an Invoice: Unpaid, Partially Paid, or Paid. It is separate from Invoice Status.
_Avoid_: Invoice Status, payment status

**Customer Credit**:
Unused value from an overpayment, owned by one Customer and linked to the originating Payment. Credit is unresolved while its available value is greater than zero.
_Avoid_: Balance, refund, payment

**Credit Application**:
An explicit amount taken from one selected Customer Credit source and applied to one Invoice.
_Avoid_: Payment, automatic credit

**Credit Refund**:
An outbound return of available Customer Credit to its Customer.
_Avoid_: Reversal, payment refund

**Reversal**:
An immutable compensating financial record that corrects an original record without editing or deleting it.
_Avoid_: Edit, deletion, cancellation
